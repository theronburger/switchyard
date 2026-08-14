package environment

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

const (
	DefaultLiveObservationInterval = 30 * time.Second
	DefaultLiveObservationTimeout  = 10 * time.Second
	DefaultLiveObservationLimit    = 32
	MaximumLiveObservationLimit    = 128
)

type LiveObserverConfig struct {
	Coordinator *Coordinator
	Interval    time.Duration
	Timeout     time.Duration
	Limit       int
}

type LiveObserver struct {
	coordinator *Coordinator
	interval    time.Duration
	timeout     time.Duration
	limit       int
	mutex       sync.Mutex
	cursor      string
}

func NewLiveObserver(config LiveObserverConfig) (*LiveObserver, error) {
	if config.Coordinator == nil || config.Coordinator.processes == nil || config.Coordinator.readiness == nil {
		return nil, ErrInvalidRequest
	}
	if config.Interval == 0 {
		config.Interval = DefaultLiveObservationInterval
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultLiveObservationTimeout
	}
	if config.Limit == 0 {
		config.Limit = DefaultLiveObservationLimit
	}
	if config.Interval <= 0 || config.Timeout <= 0 || config.Timeout > config.Interval ||
		config.Limit < 1 || config.Limit > MaximumLiveObservationLimit {
		return nil, ErrInvalidRequest
	}
	return &LiveObserver{
		coordinator: config.Coordinator,
		interval:    config.Interval,
		timeout:     config.Timeout,
		limit:       config.Limit,
	}, nil
}

// RefreshOnce processes a bounded page. The cursor rotates across pages so a
// large local inventory cannot starve later environment IDs.
func (observer *LiveObserver) RefreshOnce(ctx context.Context) error {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	page, err := observer.coordinator.refreshRunningPage(ctx, observer.cursor, observer.limit)
	if err != nil {
		return err
	}
	if len(page.Results) == 0 && observer.cursor != "" {
		observer.cursor = ""
		page, err = observer.coordinator.refreshRunningPage(ctx, "", observer.limit)
		if err != nil {
			return err
		}
	}
	if page.HasMore {
		observer.cursor = page.NextEnvironmentID
	} else {
		observer.cursor = ""
	}
	return nil
}

func (observer *LiveObserver) Run(ctx context.Context) error {
	ticker := time.NewTicker(observer.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			cycle, cancel := context.WithTimeout(ctx, observer.timeout)
			_ = observer.RefreshOnce(cycle)
			cancel()
		}
	}
}

func (coordinator *Coordinator) refreshRunningPage(
	ctx context.Context,
	afterEnvironmentID string,
	limit int,
) (CurrentEnvironmentPage, error) {
	page, err := coordinator.journal.ListCurrent(ctx, afterEnvironmentID, limit)
	if err != nil {
		return CurrentEnvironmentPage{}, err
	}
	for _, listed := range page.Results {
		if err := ctx.Err(); err != nil {
			return CurrentEnvironmentPage{}, err
		}
		if listed.State != domain.EnvironmentRunning {
			continue
		}
		lock := coordinator.lockFor(listed.EnvironmentID)
		lock.Lock()
		current, exists, currentError := coordinator.journal.Current(ctx, listed.EnvironmentID)
		if currentError == nil && exists && current.State == domain.EnvironmentRunning && current.RunID == listed.RunID {
			var refreshed EnvironmentResult
			refreshed, currentError = coordinator.observeRunningEnvironment(ctx, current)
			if currentError == nil {
				_, currentError = coordinator.journal.RefreshCurrent(ctx, refreshed)
			}
		}
		lock.Unlock()
		if currentError != nil {
			return CurrentEnvironmentPage{}, currentError
		}
	}
	return page, nil
}

func (coordinator *Coordinator) observeRunningEnvironment(
	ctx context.Context,
	current EnvironmentResult,
) (EnvironmentResult, error) {
	refreshed := cloneEnvironment(current)
	refreshed.UpdatedAt = coordinator.now().UTC()
	for index := range refreshed.Services {
		if err := ctx.Err(); err != nil {
			return EnvironmentResult{}, err
		}
		service := &refreshed.Services[index]
		observation, processError := coordinator.processes.Reconcile(ctx, service.OwnershipPath)
		if ctx.Err() != nil {
			return EnvironmentResult{}, ctx.Err()
		}
		service.Observation = serviceObservation(observation, processError, refreshed.UpdatedAt)

		report, healthError := coordinator.readiness.CheckHealth(ctx, ReadinessTarget{
			EnvironmentID: current.EnvironmentID,
			RunID:         current.RunID,
			Service:       *cloneService(service),
			Ports:         cloneLeases(current.Ports),
			Spec:          service.Readiness,
		})
		if ctx.Err() != nil {
			return EnvironmentResult{}, ctx.Err()
		}
		if healthError != nil || !validObservedHealth(report) {
			service.Health = HealthReport{Readiness: "unknown", Health: "degraded"}
			if service.Observation.Code == "" {
				service.Observation.Code = ServiceObservationHealthFailed
			}
		} else {
			service.Health = report
		}
		if processError != nil || !observation.OwnershipVerified {
			service.Health.Health = "degraded"
		} else if observation.State != "running" || observation.MemberCount == 0 {
			service.Health = HealthReport{Readiness: "not_ready", Health: "unhealthy"}
		}
	}
	return refreshed, nil
}

func serviceObservation(
	observation processhost.Observation,
	observationError error,
	observedAt time.Time,
) ServiceObservation {
	result := ServiceObservation{State: observation.State, ObservedAt: observedAt}
	if !observation.ObservedAt.IsZero() {
		result.ObservedAt = observation.ObservedAt.UTC()
	}
	if observationError != nil {
		result.State = "unverifiable"
		result.Code = ServiceObservationProcessFailed
		if errors.Is(observationError, processhost.ErrOwnershipMismatch) ||
			errors.Is(observationError, processhost.ErrOrphanUnverified) {
			result.Code = ServiceObservationOwnershipUnverified
		}
		return result
	}
	if !observation.OwnershipVerified {
		result.Code = ServiceObservationOwnershipUnverified
		return result
	}
	result.ProcessCount = observation.MemberCount
	if observation.MemoryBytes > math.MaxInt64 {
		result.MemoryBytes = math.MaxInt64
	} else {
		result.MemoryBytes = int64(observation.MemoryBytes)
	}
	// Aggregate CPU time is not an honest utilization percentage without a
	// comparable prior sample and elapsed interval. Keep v1 CPU at zero.
	result.CPUPercent = 0
	return result
}

func validObservedHealth(report HealthReport) bool {
	return (report.Readiness == "ready" || report.Readiness == "not_ready") &&
		(report.Health == "healthy" || report.Health == "unhealthy")
}

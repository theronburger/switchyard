package health

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultFailureThreshold = 3
	MaxProbesPerService     = 16
)

var ErrInvalidService = errors.New("invalid health service")

type ProbeRunner interface {
	Check(ctx context.Context, spec ProbeSpec) (ProbeResult, error)
}

type MonitorConfig struct {
	Prober  ProbeRunner
	Backoff Backoff
	Now     func() time.Time
}

type Monitor struct {
	prober  ProbeRunner
	backoff Backoff
	now     func() time.Time
}

func NewMonitor(config MonitorConfig) (*Monitor, error) {
	if config.Prober == nil {
		return nil, fmt.Errorf("%w: probe runner is required", ErrInvalidService)
	}
	if config.Backoff.Initial <= 0 || config.Backoff.Maximum < config.Backoff.Initial {
		config.Backoff = DefaultBackoff()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Monitor{prober: config.Prober, backoff: config.Backoff, now: config.Now}, nil
}

func (monitor *Monitor) Observe(
	ctx context.Context,
	spec ServiceSpec,
	previous ServiceRuntimeState,
	processRunning bool,
) (Observation, error) {
	threshold, err := validateService(spec)
	if err != nil {
		return Observation{}, err
	}
	observation := Observation{
		EnvironmentID:  spec.EnvironmentID,
		ServiceID:      spec.ServiceID,
		RunID:          spec.RunID,
		ProcessRunning: processRunning,
		Runtime:        previous,
		ObservedAt:     monitor.now(),
	}
	if !processRunning {
		observation.State = StateUnhealthy
		observation.Runtime.ConsecutiveFailures = nextFailure(previous.ConsecutiveFailures, threshold)
		observation.RetryAfter = monitor.backoff.Delay(uint(observation.Runtime.ConsecutiveFailures))
		return observation, nil
	}

	observation.Readiness, err = monitor.run(ctx, spec.Readiness)
	if err != nil {
		return Observation{}, err
	}
	observation.ReadinessReady = allSuccessful(observation.Readiness)
	if observation.ReadinessReady {
		observation.Runtime.EverReady = true
	}

	if !observation.ReadinessReady {
		observation.Runtime.ConsecutiveFailures = nextFailure(previous.ConsecutiveFailures, threshold)
		if observation.Runtime.ConsecutiveFailures >= threshold {
			observation.State = StateUnhealthy
		} else if previous.EverReady {
			observation.State = StateDegraded
		} else {
			observation.State = StateStarting
		}
		observation.RetryAfter = monitor.backoff.Delay(uint(observation.Runtime.ConsecutiveFailures))
		return observation, nil
	}

	observation.Health, err = monitor.run(ctx, spec.Health)
	if err != nil {
		return Observation{}, err
	}
	observation.HealthHealthy = allSuccessful(observation.Health)
	if !observation.HealthHealthy {
		observation.Runtime.ConsecutiveFailures = nextFailure(previous.ConsecutiveFailures, threshold)
		if observation.Runtime.ConsecutiveFailures >= threshold {
			observation.State = StateUnhealthy
		} else {
			observation.State = StateDegraded
		}
		observation.RetryAfter = monitor.backoff.Delay(uint(observation.Runtime.ConsecutiveFailures))
		return observation, nil
	}

	observation.State = StateHealthy
	observation.Runtime.ConsecutiveFailures = 0
	return observation, nil
}

func (monitor *Monitor) run(ctx context.Context, specs []ProbeSpec) ([]ProbeResult, error) {
	results := make([]ProbeResult, 0, len(specs))
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := monitor.prober.Check(ctx, spec)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func validateService(spec ServiceSpec) (int, error) {
	if spec.EnvironmentID == "" || spec.ServiceID == "" || spec.RunID == "" {
		return 0, fmt.Errorf("%w: service identity is incomplete", ErrInvalidService)
	}
	if len(spec.Readiness)+len(spec.Health) > MaxProbesPerService {
		return 0, fmt.Errorf("%w: too many probes", ErrInvalidService)
	}
	threshold := spec.FailureThreshold
	if threshold == 0 {
		threshold = DefaultFailureThreshold
	}
	if threshold < 1 || threshold > 100 {
		return 0, fmt.Errorf("%w: failure threshold is outside the allowed range", ErrInvalidService)
	}
	return threshold, nil
}

func nextFailure(previous, threshold int) int {
	if previous < 0 {
		return 1
	}
	if previous >= threshold {
		return threshold
	}
	return previous + 1
}

func allSuccessful(results []ProbeResult) bool {
	for _, result := range results {
		if !result.Success {
			return false
		}
	}
	return true
}

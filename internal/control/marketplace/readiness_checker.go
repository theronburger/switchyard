package marketplacecontrol

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/health"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

const (
	defaultReadinessWait     = 30 * time.Second
	maximumReadinessWait     = 2 * time.Minute
	defaultReadinessInterval = 250 * time.Millisecond
)

var (
	ErrReadinessInvalid = errors.New("Marketplace readiness request is invalid")
	ErrReadinessTimeout = errors.New("Marketplace service did not become ready in time")
)

type HealthProber interface {
	Check(context.Context, health.ProbeSpec) (health.ProbeResult, error)
}

type ReadinessConfig struct {
	MaximumWait  time.Duration
	Interval     time.Duration
	ProbeTimeout time.Duration
	Wait         func(context.Context, time.Duration) error
}

type ReadinessChecker struct {
	prober       HealthProber
	maximumWait  time.Duration
	interval     time.Duration
	probeTimeout time.Duration
	wait         func(context.Context, time.Duration) error
}

func NewReadinessChecker(prober HealthProber, config ReadinessConfig) (ReadinessChecker, error) {
	if prober == nil {
		return ReadinessChecker{}, ErrReadinessInvalid
	}
	if config.MaximumWait == 0 {
		config.MaximumWait = defaultReadinessWait
	}
	if config.Interval == 0 {
		config.Interval = defaultReadinessInterval
	}
	if config.ProbeTimeout == 0 {
		config.ProbeTimeout = health.DefaultProbeTimeout
	}
	if config.MaximumWait <= 0 || config.MaximumWait > maximumReadinessWait ||
		config.Interval <= 0 || config.Interval > config.MaximumWait ||
		config.ProbeTimeout <= 0 || config.ProbeTimeout > health.HardMaxProbeTimeout {
		return ReadinessChecker{}, ErrReadinessInvalid
	}
	if config.Wait == nil {
		config.Wait = waitForReadinessInterval
	}
	return ReadinessChecker{
		prober:       prober,
		maximumWait:  config.MaximumWait,
		interval:     config.Interval,
		probeTimeout: config.ProbeTimeout,
		wait:         config.Wait,
	}, nil
}

func (checker ReadinessChecker) WaitReady(
	ctx context.Context,
	target environment.ReadinessTarget,
) error {
	readiness, _, err := checker.probes(target)
	if err != nil {
		return err
	}
	waitContext, cancel := context.WithTimeout(ctx, checker.maximumWait)
	defer cancel()
	for {
		ready, err := checker.run(waitContext, readiness)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
				return ErrReadinessTimeout
			}
			return ErrReadinessInvalid
		}
		if ready {
			return nil
		}
		if err := checker.wait(waitContext, checker.interval); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(waitContext.Err(), context.DeadlineExceeded) ||
				errors.Is(err, context.DeadlineExceeded) {
				return ErrReadinessTimeout
			}
			return ErrReadinessInvalid
		}
	}
}

func (checker ReadinessChecker) CheckHealth(
	ctx context.Context,
	target environment.ReadinessTarget,
) (environment.HealthReport, error) {
	readiness, healthProbes, err := checker.probes(target)
	if err != nil {
		return environment.HealthReport{}, err
	}
	ready, err := checker.run(ctx, readiness)
	if err != nil {
		if ctx.Err() != nil {
			return environment.HealthReport{}, ctx.Err()
		}
		return environment.HealthReport{}, ErrReadinessInvalid
	}
	healthy, err := checker.run(ctx, healthProbes)
	if err != nil {
		if ctx.Err() != nil {
			return environment.HealthReport{}, ctx.Err()
		}
		return environment.HealthReport{}, ErrReadinessInvalid
	}
	report := environment.HealthReport{Readiness: "not_ready", Health: "unhealthy"}
	if ready {
		report.Readiness = "ready"
	}
	if ready && healthy {
		report.Health = "healthy"
	}
	return report, nil
}

func (checker ReadinessChecker) probes(
	target environment.ReadinessTarget,
) ([]health.ProbeSpec, []health.ProbeSpec, error) {
	if target.EnvironmentID == "" || target.RunID == "" || target.Service.ID == "" ||
		target.EnvironmentID != target.Service.EnvironmentID || target.RunID != target.Service.RunID ||
		target.Spec.ID != readinessID(target.Service.ID) {
		return nil, nil, ErrReadinessInvalid
	}
	definition, found := marketplaceadapter.DefaultCatalog().Definition(target.Service.ID)
	if !found {
		return nil, nil, ErrReadinessInvalid
	}
	leases, err := serviceLeases(target, definition)
	if err != nil {
		return nil, nil, err
	}
	readinessProbes := make([]marketplaceadapter.Probe, 0, len(definition.Readiness)+1)
	for _, infrastructure := range definition.Infrastructure {
		readinessProbes = append(readinessProbes, infrastructure.Readiness...)
	}
	readinessProbes = append(readinessProbes, definition.Readiness...)
	readiness, err := checker.convertProbes(target.Spec.ID+".ready", readinessProbes, leases)
	if err != nil {
		return nil, nil, err
	}
	healthProbes, err := checker.convertProbes(target.Spec.ID+".health", definition.Health, leases)
	if err != nil {
		return nil, nil, err
	}
	return readiness, healthProbes, nil
}

func (checker ReadinessChecker) convertProbes(
	prefix string,
	probes []marketplaceadapter.Probe,
	leases map[string]portlease.Lease,
) ([]health.ProbeSpec, error) {
	converted := make([]health.ProbeSpec, 0, len(probes))
	for index, probe := range probes {
		lease, found := leases[probe.PortRequirement]
		if !found {
			return nil, ErrReadinessInvalid
		}
		spec := health.ProbeSpec{
			ID:      prefix + "." + strconv.Itoa(index) + "." + probe.PortRequirement,
			Lease:   health.Lease{Host: lease.Host, Port: lease.Port},
			Timeout: checker.probeTimeout,
		}
		switch probe.Kind {
		case marketplaceadapter.ProbeKindTCP:
			spec.Kind = health.ProbeKindTCP
		case marketplaceadapter.ProbeKindHTTP:
			spec.Kind = health.ProbeKindHTTP
			spec.Method = probe.Method
			if spec.Method == "" {
				spec.Method = http.MethodGet
			}
			spec.Path = probe.Path
			for _, accepted := range probe.AcceptedStatuses {
				spec.AcceptedStatuses = append(spec.AcceptedStatuses, health.StatusRange{
					Minimum: accepted.Minimum,
					Maximum: accepted.Maximum,
				})
			}
		default:
			return nil, ErrReadinessInvalid
		}
		converted = append(converted, spec)
	}
	return converted, nil
}

func (checker ReadinessChecker) run(ctx context.Context, probes []health.ProbeSpec) (bool, error) {
	for _, probe := range probes {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		result, err := checker.prober.Check(ctx, probe)
		if err != nil {
			return false, err
		}
		if !result.Success {
			return false, nil
		}
	}
	return true, nil
}

func serviceLeases(
	target environment.ReadinessTarget,
	definition marketplaceadapter.ServiceDefinition,
) (map[string]portlease.Lease, error) {
	byPurpose := make(map[string]portlease.Lease)
	for _, lease := range target.Ports {
		if lease.Key.EnvironmentID != target.EnvironmentID || lease.Host != health.LoopbackHost ||
			lease.Port < 1 || lease.Port > 65535 {
			return nil, ErrReadinessInvalid
		}
		if lease.Key.ServiceID != target.Service.ID {
			continue
		}
		if _, duplicate := byPurpose[lease.Key.Purpose]; duplicate {
			return nil, ErrReadinessInvalid
		}
		byPurpose[lease.Key.Purpose] = lease
	}
	byRequirement := make(map[string]portlease.Lease, len(definition.PortRequirements))
	for _, requirement := range definition.PortRequirements {
		if lease, found := byPurpose[requirement.Purpose]; found {
			byRequirement[requirement.ID] = lease
		}
	}
	return byRequirement, nil
}

func waitForReadinessInterval(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

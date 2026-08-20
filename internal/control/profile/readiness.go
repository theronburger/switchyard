package profile

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/health"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

type HealthProber interface {
	Check(context.Context, health.ProbeSpec) (health.ProbeResult, error)
}

type ReadinessChecker struct {
	registry    Registry
	prober      HealthProber
	maximumWait time.Duration
	interval    time.Duration
}

func NewReadinessChecker(registry Registry, prober HealthProber) (ReadinessChecker, error) {
	if prober == nil {
		return ReadinessChecker{}, ErrProfileInvalid
	}
	return ReadinessChecker{registry: registry, prober: prober, maximumWait: 2 * time.Minute, interval: 250 * time.Millisecond}, nil
}

func (checker ReadinessChecker) WaitReady(ctx context.Context, target environmentcontrol.ReadinessTarget) error {
	probes, _, err := checker.probes(target)
	if err != nil {
		return err
	}
	maximumWait := checker.maximumWait
	registration, lookupErr := checker.registry.LookupPinned(target.EnvironmentID, target.ProfileDigest)
	if lookupErr != nil {
		return lookupErr
	}
	if configured := registration.Profile.Services[target.Service.ID].ReadinessTimeout; configured != "" {
		parsed, parseErr := time.ParseDuration(configured)
		if parseErr != nil {
			return ErrProfileInvalid
		}
		maximumWait = parsed
	}
	waitContext, cancel := context.WithTimeout(ctx, maximumWait)
	defer cancel()
	for {
		ready, err := checker.run(waitContext, probes)
		if err != nil {
			if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
				return errors.New("service readiness timed out")
			}
			return err
		}
		if ready {
			return nil
		}
		timer := time.NewTimer(checker.interval)
		select {
		case <-waitContext.Done():
			timer.Stop()
			return errors.New("service readiness timed out")
		case <-timer.C:
		}
	}
}

func (checker ReadinessChecker) CheckHealth(ctx context.Context, target environmentcontrol.ReadinessTarget) (environmentcontrol.HealthReport, error) {
	readiness, healthProbes, err := checker.probes(target)
	if err != nil {
		return environmentcontrol.HealthReport{}, err
	}
	ready, err := checker.run(ctx, readiness)
	if err != nil {
		return environmentcontrol.HealthReport{}, err
	}
	healthy, err := checker.run(ctx, healthProbes)
	if err != nil {
		return environmentcontrol.HealthReport{}, err
	}
	result := environmentcontrol.HealthReport{Readiness: "not_ready", Health: "unhealthy"}
	if ready {
		result.Readiness = "ready"
	}
	if ready && healthy {
		result.Health = "healthy"
	}
	return result, nil
}

func (checker ReadinessChecker) probes(target environmentcontrol.ReadinessTarget) ([]health.ProbeSpec, []health.ProbeSpec, error) {
	registration, err := checker.registry.LookupPinned(target.EnvironmentID, target.ProfileDigest)
	if err != nil || target.Service.ID == "" || target.Service.EnvironmentID != target.EnvironmentID ||
		target.Service.RunID != target.RunID || target.Spec.ID != readinessID(target.Service.ID) {
		return nil, nil, ErrProfileInvalid
	}
	service, found := registration.Profile.Services[target.Service.ID]
	if !found {
		return nil, nil, ErrProfileInvalid
	}
	readiness, err := convertProbes(target, service.Readiness)
	if err != nil {
		return nil, nil, err
	}
	healthProbes, err := convertProbes(target, service.Health)
	if err != nil {
		return nil, nil, err
	}
	return readiness, healthProbes, nil
}

func convertProbes(target environmentcontrol.ReadinessTarget, configured []configuration.Probe) ([]health.ProbeSpec, error) {
	leases := make(map[string]portlease.Lease)
	for _, lease := range target.Ports {
		if lease.Key.EnvironmentID == target.EnvironmentID && lease.Key.ServiceID == target.Service.ID {
			leases[lease.Key.Purpose] = lease
		}
	}
	result := make([]health.ProbeSpec, 0, len(configured))
	for index, probe := range configured {
		lease, found := leases[probe.Port]
		if !found {
			return nil, ErrProfileInvalid
		}
		spec := health.ProbeSpec{
			ID: target.Spec.ID + "." + strconv.Itoa(index), Lease: health.Lease{Host: lease.Host, Port: lease.Port},
			Timeout: health.DefaultProbeTimeout,
		}
		switch probe.Kind {
		case "tcp":
			spec.Kind = health.ProbeKindTCP
		case "http":
			spec.Kind = health.ProbeKindHTTP
			spec.Method = probe.Method
			if spec.Method == "" {
				spec.Method = http.MethodGet
			}
			spec.Path = probe.Path
			if spec.Path == "" {
				spec.Path = "/"
			}
			for _, accepted := range probe.AcceptedStatuses {
				spec.AcceptedStatuses = append(spec.AcceptedStatuses, health.StatusRange{Minimum: accepted.Minimum, Maximum: accepted.Maximum})
			}
		default:
			return nil, ErrProfileInvalid
		}
		result = append(result, spec)
	}
	return result, nil
}

func (checker ReadinessChecker) run(ctx context.Context, probes []health.ProbeSpec) (bool, error) {
	for _, probe := range probes {
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

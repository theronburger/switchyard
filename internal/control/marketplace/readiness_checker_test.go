package marketplacecontrol

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/health"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

type scriptedHealthProber struct {
	results []health.ProbeResult
	errors  []error
	specs   []health.ProbeSpec
}

func (prober *scriptedHealthProber) Check(
	_ context.Context,
	spec health.ProbeSpec,
) (health.ProbeResult, error) {
	index := len(prober.specs)
	prober.specs = append(prober.specs, spec)
	if index < len(prober.errors) && prober.errors[index] != nil {
		return health.ProbeResult{}, prober.errors[index]
	}
	if index < len(prober.results) {
		result := prober.results[index]
		result.ProbeID = spec.ID
		result.Kind = spec.Kind
		return result, nil
	}
	return health.ProbeResult{ProbeID: spec.ID, Kind: spec.Kind, Success: true, Code: health.ResultOK}, nil
}

func TestReadinessCheckerRetriesBoundedlyOnAssignedPort(t *testing.T) {
	t.Parallel()
	prober := &scriptedHealthProber{results: []health.ProbeResult{
		{Code: health.ResultUnavailable},
		{Code: health.ResultOK, Success: true},
	}}
	waits := 0
	checker, err := NewReadinessChecker(prober, ReadinessConfig{
		MaximumWait:  time.Second,
		Interval:     time.Millisecond,
		ProbeTimeout: 25 * time.Millisecond,
		Wait: func(ctx context.Context, _ time.Duration) error {
			waits++
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := readinessTarget("env_ready", "run_ready", "organizer", 30101, 0)
	if err := checker.WaitReady(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if waits != 1 || len(prober.specs) != 2 {
		t.Fatalf("bounded retry: waits=%d probes=%#v", waits, prober.specs)
	}
	for _, spec := range prober.specs {
		if spec.Kind != health.ProbeKindTCP || spec.Lease != (health.Lease{Host: "127.0.0.1", Port: 30101}) ||
			spec.Timeout != 25*time.Millisecond {
			t.Fatalf("readiness did not use the assigned lease: %#v", spec)
		}
	}
}

func TestReadinessCheckerSeparatesReadinessAndHealthProbes(t *testing.T) {
	t.Parallel()
	prober := &scriptedHealthProber{}
	checker, err := NewReadinessChecker(prober, ReadinessConfig{
		MaximumWait: time.Second,
		Interval:    time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := readinessTarget("env_health", "run_health", "nonprofit-service", 30201, 31201)
	report, err := checker.CheckHealth(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if report != (environment.HealthReport{Readiness: "ready", Health: "healthy"}) {
		t.Fatalf("health report: %#v", report)
	}
	if len(prober.specs) != 4 {
		t.Fatalf("probe count: %#v", prober.specs)
	}
	want := []struct {
		kind health.ProbeKind
		port int
	}{
		{health.ProbeKindTCP, 32201},
		{health.ProbeKindTCP, 30201},
		{health.ProbeKindTCP, 31201},
		{health.ProbeKindHTTP, 30201},
	}
	for index, expected := range want {
		spec := prober.specs[index]
		if spec.Kind != expected.kind || spec.Lease.Port != expected.port {
			t.Fatalf("probe %d: got %#v want %#v", index, spec, expected)
		}
	}
	httpProbe := prober.specs[3]
	if httpProbe.Method != "GET" || httpProbe.Path != "/" ||
		!reflect.DeepEqual(httpProbe.AcceptedStatuses, []health.StatusRange{{Minimum: 200, Maximum: 499}}) {
		t.Fatalf("HTTP health probe changed: %#v", httpProbe)
	}
}

func TestReadinessCheckerHonorsCancellationAndTimeout(t *testing.T) {
	t.Parallel()
	prober := &scriptedHealthProber{results: []health.ProbeResult{{Code: health.ResultUnavailable}}}
	var cancel context.CancelFunc
	checker, err := NewReadinessChecker(prober, ReadinessConfig{
		MaximumWait: time.Second,
		Interval:    time.Millisecond,
		Wait: func(ctx context.Context, _ time.Duration) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelContext := context.WithCancel(context.Background())
	cancel = cancelContext
	err = checker.WaitReady(ctx, readinessTarget("env_cancel", "run_cancel", "organizer", 30301, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled readiness: %v", err)
	}

	timeoutProber := &scriptedHealthProber{results: []health.ProbeResult{{Code: health.ResultUnavailable}}}
	timeoutChecker, err := NewReadinessChecker(timeoutProber, ReadinessConfig{
		MaximumWait: time.Second,
		Interval:    time.Millisecond,
		Wait: func(context.Context, time.Duration) error {
			return context.DeadlineExceeded
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = timeoutChecker.WaitReady(
		context.Background(),
		readinessTarget("env_timeout", "run_timeout", "organizer", 30401, 0),
	)
	if !errors.Is(err, ErrReadinessTimeout) {
		t.Fatalf("bounded timeout: %v", err)
	}
}

func TestReadinessCheckerRejectsHostileTargetsWithoutLeaking(t *testing.T) {
	t.Parallel()
	checker, err := NewReadinessChecker(&scriptedHealthProber{}, ReadinessConfig{})
	if err != nil {
		t.Fatal(err)
	}
	target := readinessTarget("env_safe", "run_safe", "organizer", 30501, 0)
	target.Spec.ID = "AWS_SECRET_ACCESS_KEY=secret-token@example.invalid"
	err = checker.WaitReady(context.Background(), target)
	if !errors.Is(err, ErrReadinessInvalid) || strings.Contains(strings.ToLower(err.Error()), "secret") ||
		strings.Contains(err.Error(), "@") {
		t.Fatalf("readiness error leaked target content: %v", err)
	}
}

func readinessTarget(
	environmentID string,
	runID string,
	serviceID string,
	httpPort int,
	lambdaPort int,
) environment.ReadinessTarget {
	ports := []portlease.Lease{{
		Key:  portlease.Key{EnvironmentID: environmentID, ServiceID: serviceID, Purpose: "http"},
		Host: "127.0.0.1", Port: httpPort,
	}}
	if serviceID == "nonprofit-service" {
		ports = append(ports,
			portlease.Lease{
				Key:  portlease.Key{EnvironmentID: environmentID, ServiceID: serviceID, Purpose: "lambda"},
				Host: "127.0.0.1", Port: lambdaPort,
			},
			portlease.Lease{
				Key: portlease.Key{
					EnvironmentID: environmentID,
					ServiceID:     serviceID,
					Purpose:       "elasticmq-rest",
				},
				Host: "127.0.0.1", Port: httpPort + 2000,
			},
		)
	}
	return environment.ReadinessTarget{
		EnvironmentID: environmentID,
		RunID:         runID,
		Service: environment.ServiceResult{
			ID:            serviceID,
			EnvironmentID: environmentID,
			RunID:         runID,
			Owned:         true,
			Process: processhost.Ownership{
				EnvironmentID: environmentID,
				ServiceID:     serviceID,
				RunID:         runID,
			},
		},
		Ports: ports,
		Spec:  environment.ReadinessSpec{ID: readinessID(serviceID)},
	}
}

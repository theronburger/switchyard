package profile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

func pinnedRegistration(t *testing.T, current Registration) Registration {
	t.Helper()
	pinned := current
	pinned.ProfileDigest = "sha256:" + strings.Repeat("b", 64)
	// The pinned profile differs from the head: it still knows a service the
	// head no longer declares and uses a shorter readiness timeout.
	services := make(map[string]configuration.Service, len(current.Profile.Services)+1)
	for id, service := range current.Profile.Services {
		services[id] = service
	}
	legacy := services["web"]
	legacy.ReadinessTimeout = "5ms"
	legacy.Readiness = []configuration.Probe{{Kind: "tcp", Port: "http"}}
	services["legacy-worker"] = configuration.Service{
		DisplayName: "Legacy worker", Kind: "worker",
		Command: configuration.Command{Executable: "/usr/bin/true", WorkingDirectory: ".", Timeout: "30s"},
	}
	services["web"] = legacy
	pinned.Profile.Services = services
	return pinned
}

func TestRegistryResolvesPinnedDigestsWithoutFallingBackToHead(t *testing.T) {
	current := profileRegistration(t)
	pinned := pinnedRegistration(t, current)
	registry, err := NewRegistry([]Registration{current}, pinned)
	if err != nil {
		t.Fatal(err)
	}
	head, err := registry.LookupPinned(current.EnvironmentID, current.ProfileDigest)
	if err != nil || head.ProfileDigest != current.ProfileDigest {
		t.Fatalf("head lookup: %+v err=%v", head.ProfileDigest, err)
	}
	legacy, err := registry.LookupPinned(current.EnvironmentID, "")
	if err != nil || legacy.ProfileDigest != current.ProfileDigest {
		t.Fatalf("empty digest must select the current registration: %v", err)
	}
	resolved, err := registry.LookupPinned(current.EnvironmentID, pinned.ProfileDigest)
	if err != nil || resolved.ProfileDigest != pinned.ProfileDigest || resolved.Profile.Services["legacy-worker"].DisplayName != "Legacy worker" {
		t.Fatalf("pinned lookup: %+v err=%v", resolved.ProfileDigest, err)
	}
	if _, err := registry.LookupPinned(current.EnvironmentID, "sha256:"+strings.Repeat("c", 64)); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("unknown digest must not resolve: %v", err)
	}
	if _, err := registry.LookupPinned("environment_other", pinned.ProfileDigest); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("unknown environment must not resolve: %v", err)
	}
	if digests := registry.PinnedDigests(current.EnvironmentID); len(digests) != 1 || digests[0] != pinned.ProfileDigest {
		t.Fatalf("pinned digests: %v", digests)
	}
}

func TestRegistryRejectsInconsistentPinnedRegistrations(t *testing.T) {
	current := profileRegistration(t)
	cases := map[string]func(Registration) Registration{
		"same digest as head":   func(pinned Registration) Registration { pinned.ProfileDigest = current.ProfileDigest; return pinned },
		"unknown environment":   func(pinned Registration) Registration { pinned.EnvironmentID = "environment_other"; return pinned },
		"different profile key": func(pinned Registration) Registration { pinned.ProfileKey = "other"; return pinned },
		"different worktree":    func(pinned Registration) Registration { pinned.WorktreeID = "worktree_02"; return pinned },
		"malformed digest":      func(pinned Registration) Registration { pinned.ProfileDigest = "plain"; return pinned },
	}
	for name, mutate := range cases {
		pinned := mutate(pinnedRegistration(t, current))
		if _, err := NewRegistry([]Registration{current}, pinned); !errors.Is(err, ErrProfileInvalid) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	duplicate := pinnedRegistration(t, current)
	if _, err := NewRegistry([]Registration{current}, duplicate, duplicate); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("duplicate pinned digest: got %v", err)
	}
}

func TestPlanBuilderCompilesFromThePinnedPayload(t *testing.T) {
	current := profileRegistration(t)
	pinned := pinnedRegistration(t, current)
	registry, err := NewRegistry([]Registration{current}, pinned)
	if err != nil {
		t.Fatal(err)
	}
	builder := NewPlanBuilder(registry)
	lease := portlease.Lease{
		Key:  portlease.Key{EnvironmentID: current.EnvironmentID, ServiceID: "web", Purpose: "http"},
		Host: "127.0.0.1", Port: 31001,
	}
	plan, err := builder.Build(environmentcontrol.PlanningRequest{
		EnvironmentID: current.EnvironmentID, RunID: "run_01",
		Intent: environmentcontrol.PlanIntent{ProfileDigest: pinned.ProfileDigest, TargetID: "local", ServiceIDs: []string{"legacy-worker"}},
	})
	if err != nil || len(plan.ServiceStages) != 1 || plan.ServiceStages[0][0].ID != "legacy-worker" {
		t.Fatalf("pinned plan: %+v err=%v", plan, err)
	}
	if _, err := builder.Build(environmentcontrol.PlanningRequest{
		EnvironmentID: current.EnvironmentID, RunID: "run_02",
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: current.ProfileDigest, TargetID: "local", ServiceIDs: []string{"legacy-worker"}},
		AssignedPorts: []portlease.Lease{lease},
	}); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("head must not know the pinned-only service: %v", err)
	}
	if _, err := builder.Build(environmentcontrol.PlanningRequest{
		EnvironmentID: current.EnvironmentID, RunID: "run_03",
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: "", TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{lease},
	}); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("a start without a pinned digest must be refused: %v", err)
	}
}

func TestReadinessUsesThePinnedProfile(t *testing.T) {
	current := profileRegistration(t)
	pinned := pinnedRegistration(t, current)
	registry, err := NewRegistry([]Registration{current}, pinned)
	if err != nil {
		t.Fatal(err)
	}
	checker, err := NewReadinessChecker(registry, neverReadyProber{})
	if err != nil {
		t.Fatal(err)
	}
	checker.maximumWait = time.Minute
	checker.interval = time.Millisecond
	target := environmentcontrol.ReadinessTarget{
		EnvironmentID: current.EnvironmentID, RunID: "run_01", ProfileDigest: pinned.ProfileDigest,
		Service: environmentcontrol.ServiceResult{ID: "web", EnvironmentID: current.EnvironmentID, RunID: "run_01"},
		Ports:   []portlease.Lease{{Key: portlease.Key{EnvironmentID: current.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}},
		Spec:    environmentcontrol.ReadinessSpec{ID: readinessID("web")},
	}
	// The pinned profile's 5ms readiness timeout applies, not the head's
	// one-minute default, which proves the probe set came from the pin.
	done := make(chan error, 1)
	go func() { done <- checker.WaitReady(context.Background(), target) }()
	select {
	case err := <-done:
		if err == nil || err.Error() != "service readiness timed out" {
			t.Fatalf("pinned readiness: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("readiness did not honour the pinned timeout")
	}
	target.ProfileDigest = "sha256:" + strings.Repeat("c", 64)
	if err := checker.WaitReady(context.Background(), target); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("unknown pinned digest must fail closed: %v", err)
	}
}

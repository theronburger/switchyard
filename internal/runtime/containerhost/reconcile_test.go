package containerhost

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReconcilerRevalidatesFullIdentityBeforeStopAndRemove(t *testing.T) {
	identity := testIdentity("apply")
	resource := ownedResource(ResourceContainer, "container-id", "switchyard-apply", identity, true)
	inventory, err := NewInventory([]Resource{resource})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: resource.Name, Identity: identity, DesiredState: DesiredAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{testing: t, runs: []scriptedRun{
		{command: plan.Actions[0].Command},
		{command: plan.Actions[1].Command},
	}}
	resources := &staticResources{
		inventory:   inventory,
		inspections: []inspectionResult{{resource: resource}, {resource: resource}},
	}
	err = (Reconciler{Runner: runner, Resources: resources}).Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if resources.inspectSeen != 2 {
		t.Fatalf("identity inspections: got %d, want 2", resources.inspectSeen)
	}
}

func TestReconcilerNeverTouchesAResourceThatTurnsForeign(t *testing.T) {
	identity := testIdentity("foreign-survival")
	owned := ownedResource(ResourceContainer, "container-id", "foreign-survival", identity, true)
	inventory, err := NewInventory([]Resource{owned})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: owned.Name, Identity: identity, DesiredState: DesiredAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	becameForeign := owned
	becameForeign.Labels = map[string]string{"team": "marketplace"}
	runner := &scriptedRunner{testing: t}
	resources := &staticResources{
		inventory:   inventory,
		inspections: []inspectionResult{{resource: becameForeign}},
	}
	err = (Reconciler{Runner: runner, Resources: resources}).Apply(context.Background(), plan)
	if !errors.Is(err, ErrOwnershipUnverified) {
		t.Fatalf("error: got %v, want %v", err, ErrOwnershipUnverified)
	}
	if len(runner.seen) != 0 {
		t.Fatalf("foreign resource received commands: %+v", runner.seen)
	}
}

func TestReconcilerVerifiesAtomicLabelsAfterCreateBeforeStart(t *testing.T) {
	identity := testIdentity("created")
	inventory, err := NewInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: "switchyard-created", Image: "elasticmq:1.6.16",
		Identity: identity, DesiredState: DesiredRunning,
	}})
	if err != nil {
		t.Fatal(err)
	}
	partial := Resource{
		Kind: ResourceContainer, ID: "created-id", Name: "switchyard-created",
		Labels: map[string]string{LabelManagedBy: ManagedByValue, LabelEnvironmentID: identity.EnvironmentID},
	}
	runner := &scriptedRunner{testing: t, runs: []scriptedRun{{command: plan.Actions[0].Command}}}
	resources := &staticResources{
		inventory: inventory, inspections: []inspectionResult{{resource: partial}},
	}
	err = (Reconciler{Runner: runner, Resources: resources}).Apply(context.Background(), plan)
	if !errors.Is(err, ErrOwnershipUnverified) {
		t.Fatalf("error: got %v, want %v", err, ErrOwnershipUnverified)
	}
	runner.assertDone()
	if len(runner.seen) != 1 || runner.seen[0].Arguments[1] != "create" {
		t.Fatalf("partial create was started: %+v", runner.seen)
	}
}

func TestReconcilerHonorsCancellationBeforeMutation(t *testing.T) {
	identity := testIdentity("cancel")
	resource := ownedResource(ResourceContainer, "container-id", "cancel", identity, true)
	inventory, err := NewInventory([]Resource{resource})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: resource.Name, Identity: identity, DesiredState: DesiredStopped,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &scriptedRunner{testing: t}
	err = (Reconciler{
		Runner: runner, Resources: &staticResources{inventory: inventory},
	}).Apply(ctx, plan)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: got %v, want cancellation", err)
	}
	if len(runner.seen) != 0 {
		t.Fatalf("cancelled plan executed commands: %+v", runner.seen)
	}
}

func TestReconcilerRedactsRunnerFailures(t *testing.T) {
	identity := testIdentity("redact")
	resource := ownedResource(ResourceContainer, "container-id", "redact", identity, true)
	inventory, err := NewInventory([]Resource{resource})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: resource.Name, Identity: identity, DesiredState: DesiredStopped,
	}})
	if err != nil {
		t.Fatal(err)
	}
	secret := "registry-token-must-not-appear"
	runner := &scriptedRunner{testing: t, runs: []scriptedRun{{
		command: plan.Actions[0].Command, err: errors.New("failed with " + secret),
	}}}
	resources := &staticResources{
		inventory: inventory, inspections: []inspectionResult{{resource: resource}},
	}
	err = (Reconciler{Runner: runner, Resources: resources}).Apply(context.Background(), plan)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unredacted error: %v", err)
	}
}

func TestReconcilerRejectsTamperedOrGlobalPruneCommands(t *testing.T) {
	identity := testIdentity("tampered")
	resource := ownedResource(ResourceContainer, "container-id", "tampered", identity, true)
	inventory, err := NewInventory([]Resource{resource})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: resource.Name, Identity: identity, DesiredState: DesiredStopped,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan.Actions[0].Command.Arguments = []string{"system", "prune", "--all", "--force"}
	runner := &scriptedRunner{testing: t}
	err = (Reconciler{
		Runner: runner, Resources: &staticResources{inventory: inventory},
	}).Apply(context.Background(), plan)
	if !errors.Is(err, ErrPlanInvalid) {
		t.Fatalf("error: got %v, want %v", err, ErrPlanInvalid)
	}
	if len(runner.seen) != 0 {
		t.Fatalf("tampered plan executed commands: %+v", runner.seen)
	}
}

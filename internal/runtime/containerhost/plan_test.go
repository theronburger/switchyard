package containerhost

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPlannerCreatesResourcesWithTheCompleteOwnershipSetAtomically(t *testing.T) {
	identity := testIdentity("create")
	inventory, err := NewInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{DockerBinary: "docker-test", Now: func() time.Time {
		return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	}}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: "switchyard-create", Image: "elasticmq:1.6.16",
		Identity: identity, DesiredState: DesiredRunning,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 2 || plan.Actions[0].Kind != ActionCreate || plan.Actions[1].Kind != ActionStart {
		t.Fatalf("actions: %+v", plan.Actions)
	}
	wantCreateArguments := []string{
		"container", "create", "--name", "switchyard-create",
		"--label", LabelManagedBy + "=" + ManagedByValue,
		"--label", LabelEnvironmentID + "=" + identity.EnvironmentID,
		"--label", LabelServiceID + "=" + identity.ServiceID,
		"--label", LabelRunID + "=" + identity.RunID,
		"--label", LabelInstanceID + "=" + identity.InstanceID,
		"elasticmq:1.6.16",
	}
	if plan.Actions[0].Command.Executable != "docker-test" ||
		!slices.Equal(plan.Actions[0].Command.Arguments, wantCreateArguments) {
		t.Fatalf("create command: %+v", plan.Actions[0].Command)
	}
	if got := plan.Actions[1].Command.Arguments; !slices.Equal(got, []string{
		"container", "start", "--", "switchyard-create",
	}) {
		t.Fatalf("start command: %v", got)
	}
}

func TestPlannerCreatesVolumesAndNetworksWithAtomicLabels(t *testing.T) {
	volumeIdentity := testIdentity("volume")
	networkIdentity := testIdentity("network")
	inventory, err := NewInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{
		{Kind: ResourceVolume, Name: "switchyard-volume", Identity: volumeIdentity, DesiredState: DesiredRunning},
		{Kind: ResourceNetwork, Name: "switchyard-network", Identity: networkIdentity, DesiredState: DesiredRunning},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 2 {
		t.Fatalf("actions: %+v", plan.Actions)
	}
	wantByKind := map[ResourceKind][]string{
		ResourceVolume: append(
			append([]string{"volume", "create"}, ownershipLabelArguments(volumeIdentity)...),
			"switchyard-volume"),
		ResourceNetwork: append(
			append([]string{"network", "create"}, ownershipLabelArguments(networkIdentity)...),
			"switchyard-network"),
	}
	for _, action := range plan.Actions {
		if !slices.Equal(action.Command.Arguments, wantByKind[action.ResourceKind]) {
			t.Fatalf("%s create command: %v", action.ResourceKind, action.Command.Arguments)
		}
	}
}

func TestPlannerGeneratesVerifiedStopAndRemoveActionsForOwnedResources(t *testing.T) {
	identity := testIdentity("remove")
	inventory, err := NewInventory([]Resource{
		ownedResource(ResourceContainer, "container-id", "switchyard-remove", identity, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: "switchyard-remove", Identity: identity, DesiredState: DesiredAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 2 || plan.Actions[0].Kind != ActionStop || plan.Actions[1].Kind != ActionRemove {
		t.Fatalf("actions: %+v", plan.Actions)
	}
	if !plan.Actions[0].Destructive() || !plan.Actions[1].Destructive() {
		t.Fatal("stop/remove actions were not classified as destructive")
	}
	if got := plan.Actions[0].Command.Arguments; !slices.Equal(got, []string{
		"container", "stop", "--time", "10", "--", "container-id",
	}) {
		t.Fatalf("stop command: %v", got)
	}
	if got := plan.Actions[1].Command.Arguments; !slices.Equal(got, []string{
		"container", "rm", "--", "container-id",
	}) {
		t.Fatalf("remove command: %v", got)
	}
}

func TestPlannerStartsAnExistingContainerWithoutRequiringItsImage(t *testing.T) {
	identity := testIdentity("restart-existing")
	resource := ownedResource(ResourceContainer, "container-id", "restart-existing", identity, false)
	inventory, err := NewInventory([]Resource{resource})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: resource.Name, Identity: identity, DesiredState: DesiredRunning,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != ActionStart {
		t.Fatalf("actions: %+v", plan.Actions)
	}
}

func TestPlannerProtectsForeignPartialAndSpoofedNameCollisions(t *testing.T) {
	identity := testIdentity("collision")
	tests := []struct {
		name     string
		labels   map[string]string
		wantCode ProtectionCode
	}{
		{name: "foreign", labels: map[string]string{"team": "marketplace"}, wantCode: ProtectionForeignCollision},
		{name: "partial", labels: map[string]string{
			LabelManagedBy: ManagedByValue, LabelEnvironmentID: identity.EnvironmentID,
		}, wantCode: ProtectionUnsafeLabels},
		{name: "spoofed", labels: map[string]string{
			LabelEnvironmentID: identity.EnvironmentID, LabelServiceID: identity.ServiceID,
			LabelRunID: identity.RunID, LabelInstanceID: identity.InstanceID,
		}, wantCode: ProtectionUnsafeLabels},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inventory, err := NewInventory([]Resource{{
				Kind: ResourceContainer, ID: "existing", Name: "elasticmq", Labels: test.labels,
			}})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := (Planner{}).Build(inventory, []Goal{{
				Kind: ResourceContainer, Name: "elasticmq", Image: "elasticmq:1.6.16",
				Identity: identity, DesiredState: DesiredRunning,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Actions) != 0 {
				t.Fatalf("unsafe resource received actions: %+v", plan.Actions)
			}
			if len(plan.Protections) != 1 || plan.Protections[0].Code != test.wantCode {
				t.Fatalf("protections: %+v", plan.Protections)
			}
		})
	}
}

func TestPlannerRefusesDuplicateOwnedIdentities(t *testing.T) {
	identity := testIdentity("duplicate-plan")
	inventory, err := NewInventory([]Resource{
		ownedResource(ResourceContainer, "one", "one", identity, true),
		ownedResource(ResourceContainer, "two", "two", identity, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: "one", Identity: identity, DesiredState: DesiredAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 0 || len(plan.Protections) != 1 ||
		plan.Protections[0].Code != ProtectionDuplicateIdentity {
		t.Fatalf("plan: %+v", plan)
	}
}

func TestPlannerNeverGeneratesGlobalPrune(t *testing.T) {
	identity := testIdentity("no-prune")
	inventory, err := NewInventory([]Resource{
		ownedResource(ResourceContainer, "container", "container", identity, true),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: "container", Identity: identity, DesiredState: DesiredAbsent,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range plan.Actions {
		if strings.Contains(strings.Join(action.Command.Arguments, " "), "prune") {
			t.Fatalf("plan generated a prune command: %+v", action.Command)
		}
	}
}

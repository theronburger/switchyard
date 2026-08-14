package containerhost

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPlannerCreatesResourcesWithTheCompleteOwnershipSetAtomically(t *testing.T) {
	identity := testIdentity("create")
	requestedBindings := []PortBinding{
		{Host: LoopbackHostIPv4, HostPort: 19325, ContainerPort: 9325, Protocol: PortProtocolTCP},
		{Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP},
	}
	inventory, err := NewInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{DockerBinary: "docker-test", Now: func() time.Time {
		return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	}}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: "switchyard-create", Image: "elasticmq:1.6.16",
		PortBindings: requestedBindings, Identity: identity, DesiredState: DesiredRunning,
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
		"--publish", "127.0.0.1:19324:9324/tcp",
		"--publish", "127.0.0.1:19325:9325/tcp",
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
	wantBindings := []PortBinding{requestedBindings[1], requestedBindings[0]}
	for _, action := range plan.Actions {
		if !slices.Equal(action.PortBindings, wantBindings) {
			t.Fatalf("canonical action bindings: got %+v, want %+v", action.PortBindings, wantBindings)
		}
	}
}

func TestPlannerRejectsUnsafeOrAmbiguousPortBindings(t *testing.T) {
	valid := PortBinding{
		Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP,
	}
	tests := []struct {
		name     string
		kind     ResourceKind
		bindings []PortBinding
	}{
		{name: "all interfaces", kind: ResourceContainer, bindings: []PortBinding{{Host: "0.0.0.0", HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP}}},
		{name: "hostname loopback", kind: ResourceContainer, bindings: []PortBinding{{Host: "localhost", HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP}}},
		{name: "other loopback", kind: ResourceContainer, bindings: []PortBinding{{Host: "127.0.0.2", HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP}}},
		{name: "zero host port", kind: ResourceContainer, bindings: []PortBinding{{Host: LoopbackHostIPv4, HostPort: 0, ContainerPort: 9324, Protocol: PortProtocolTCP}}},
		{name: "large host port", kind: ResourceContainer, bindings: []PortBinding{{Host: LoopbackHostIPv4, HostPort: 65536, ContainerPort: 9324, Protocol: PortProtocolTCP}}},
		{name: "zero target port", kind: ResourceContainer, bindings: []PortBinding{{Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 0, Protocol: PortProtocolTCP}}},
		{name: "large target port", kind: ResourceContainer, bindings: []PortBinding{{Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 65536, Protocol: PortProtocolTCP}}},
		{name: "udp", kind: ResourceContainer, bindings: []PortBinding{{Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocol("udp")}}},
		{name: "empty protocol", kind: ResourceContainer, bindings: []PortBinding{{Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9324}}},
		{name: "duplicate host port", kind: ResourceContainer, bindings: []PortBinding{valid, {Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9325, Protocol: PortProtocolTCP}}},
		{name: "duplicate target port", kind: ResourceContainer, bindings: []PortBinding{valid, {Host: LoopbackHostIPv4, HostPort: 19325, ContainerPort: 9324, Protocol: PortProtocolTCP}}},
		{name: "volume port", kind: ResourceVolume, bindings: []PortBinding{valid}},
		{name: "network port", kind: ResourceNetwork, bindings: []PortBinding{valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			image := "elasticmq:1.6.16"
			if test.kind != ResourceContainer {
				image = ""
			}
			_, err := (Planner{}).Build(Inventory{}, []Goal{{
				Kind: test.kind, Name: "unsafe-port-goal", Image: image, PortBindings: test.bindings,
				Identity: testIdentity("unsafe"), DesiredState: DesiredRunning,
			}})
			if err == nil {
				t.Fatal("unsafe port binding was accepted")
			}
		})
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

func TestPlannerStartsAnExistingContainerOnlyWhenItsImmutableConfigurationMatches(t *testing.T) {
	identity := testIdentity("restart-existing")
	resource := ownedResource(ResourceContainer, "container-id", "restart-existing", identity, false)
	inventory, err := NewInventory([]Resource{resource})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{}).Build(inventory, []Goal{{
		Kind: ResourceContainer, Name: resource.Name, Image: resource.Image,
		Identity: identity, DesiredState: DesiredRunning,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != ActionStart {
		t.Fatalf("actions: %+v", plan.Actions)
	}
}

func TestPlannerProtectsOwnedContainersWithImmutableDrift(t *testing.T) {
	identity := testIdentity("immutable-drift")
	wantBindings := []PortBinding{{
		Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP,
	}}
	matching := ownedResource(ResourceContainer, "container-id", "immutable-drift", identity, true)
	matching.PortBindings = clonePortBindings(wantBindings)
	matching.PublishedPortBindings = clonePortBindings(wantBindings)
	tests := []struct {
		name   string
		mutate func(*Resource)
	}{
		{name: "image", mutate: func(resource *Resource) { resource.Image = "elasticmq:old" }},
		{name: "configured binding", mutate: func(resource *Resource) { resource.PortBindings[0].HostPort++ }},
		{name: "published binding", mutate: func(resource *Resource) { resource.PublishedPortBindings[0].HostPort++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := matching
			resource.PortBindings = clonePortBindings(matching.PortBindings)
			resource.PublishedPortBindings = clonePortBindings(matching.PublishedPortBindings)
			test.mutate(&resource)
			inventory, err := NewInventory([]Resource{resource})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := (Planner{}).Build(inventory, []Goal{{
				Kind: ResourceContainer, Name: resource.Name, Image: matching.Image,
				PortBindings: wantBindings, Identity: identity, DesiredState: DesiredRunning,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Actions) != 0 || len(plan.Protections) != 1 ||
				plan.Protections[0].Code != ProtectionImmutableMismatch {
				t.Fatalf("drift plan: %+v", plan)
			}
		})
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

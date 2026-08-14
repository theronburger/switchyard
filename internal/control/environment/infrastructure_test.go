package environment

import (
	"context"
	"errors"
	"testing"

	"github.com/theronburger/switchyard/internal/runtime/containerhost"
)

type staticContainerResources struct {
	inventory containerhost.Inventory
}

func (resources staticContainerResources) Inventory(context.Context) (containerhost.Inventory, error) {
	return resources.inventory, nil
}

func (staticContainerResources) Inspect(
	context.Context,
	containerhost.ResourceKind,
	string,
) (containerhost.Resource, error) {
	return containerhost.Resource{}, errors.New("not used")
}

type recordingPlanApplier struct {
	calls int
}

func (applier *recordingPlanApplier) Apply(context.Context, containerhost.Plan) error {
	applier.calls++
	return nil
}

func TestContainerInfrastructureHostDoesNotApplyProtectedForeignCollision(t *testing.T) {
	inventory, err := containerhost.NewInventory([]containerhost.Resource{{
		Kind: containerhost.ResourceContainer, ID: "foreign-id", Name: "switchyard-queue",
		Labels: map[string]string{"team": "marketplace"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	identity := containerhost.Identity{
		EnvironmentID: "env_container", ServiceID: "infra_queue",
		RunID: "run_container", InstanceID: "queue_primary",
	}
	applier := &recordingPlanApplier{}
	host := ContainerInfrastructureHost{
		Resources: staticContainerResources{inventory: inventory},
		Planner:   containerhost.Planner{},
		Applier:   applier,
	}

	err = host.Ensure(context.Background(), []containerhost.Goal{{
		Kind: containerhost.ResourceContainer, Name: "switchyard-queue",
		Image: "elasticmq:latest", Identity: identity, DesiredState: containerhost.DesiredRunning,
	}})
	if !errors.Is(err, ErrProtectedInfra) {
		t.Fatalf("ensure error: got %v, want %v", err, ErrProtectedInfra)
	}
	if applier.calls != 0 {
		t.Fatalf("protected foreign resource received %d apply calls", applier.calls)
	}
}

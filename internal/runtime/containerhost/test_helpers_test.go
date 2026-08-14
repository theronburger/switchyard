package containerhost

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

type scriptedRun struct {
	command Command
	output  string
	err     error
}

type scriptedRunner struct {
	testing *testing.T
	mutex   sync.Mutex
	runs    []scriptedRun
	seen    []Command
}

func (runner *scriptedRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	runner.testing.Helper()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	runner.seen = append(runner.seen, command.Clone())
	if len(runner.runs) == 0 {
		runner.testing.Fatalf("unexpected command: %+v", command)
	}
	next := runner.runs[0]
	runner.runs = runner.runs[1:]
	if !reflect.DeepEqual(command, next.command) {
		runner.testing.Fatalf("command:\n got: %#v\nwant: %#v", command, next.command)
	}
	return []byte(next.output), next.err
}

func (runner *scriptedRunner) assertDone() {
	runner.testing.Helper()
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	if len(runner.runs) != 0 {
		runner.testing.Fatalf("%d scripted command(s) were not run", len(runner.runs))
	}
}

func testIdentity(suffix string) Identity {
	return Identity{
		EnvironmentID: "env_" + suffix,
		ServiceID:     "service_" + suffix,
		RunID:         "run_" + suffix,
		InstanceID:    "instance_" + suffix,
	}
}

func labelsJSON(t *testing.T, labels map[string]string) string {
	t.Helper()
	contents, err := json.Marshal(labels)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func ownedResource(kind ResourceKind, id, name string, identity Identity, running bool) Resource {
	return Resource{
		Kind: kind, ID: id, Name: name, Running: running,
		State:  map[bool]string{true: "running", false: "exited"}[running],
		Labels: identity.Labels(),
	}
}

type staticResources struct {
	inventory   Inventory
	inspections []inspectionResult
	inspectSeen int
}

type inspectionResult struct {
	resource Resource
	err      error
}

func (resources *staticResources) Inventory(context.Context) (Inventory, error) {
	return resources.inventory, nil
}

func (resources *staticResources) Inspect(
	_ context.Context,
	kind ResourceKind,
	reference string,
) (Resource, error) {
	if resources.inspectSeen >= len(resources.inspections) {
		return Resource{}, fmt.Errorf("unexpected inspection for %s %s", kind, reference)
	}
	result := resources.inspections[resources.inspectSeen]
	resources.inspectSeen++
	if result.err != nil {
		return Resource{}, result.err
	}
	inventory, err := NewInventory([]Resource{result.resource})
	if err != nil {
		return Resource{}, err
	}
	return inventory.Resources[0], nil
}

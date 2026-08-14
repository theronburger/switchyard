package containerhost

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDockerInventoryUsesExactArgvAndClassifiesEveryResource(t *testing.T) {
	identity := testIdentity("inventory")
	runner := &scriptedRunner{testing: t, runs: []scriptedRun{
		{
			command: Command{Executable: "docker-test", Arguments: []string{"container", "ls", "--all", "--quiet", "--no-trunc"}},
			output:  "container-owned\ncontainer-foreign\n",
		},
		{
			command: Command{Executable: "docker-test", Arguments: []string{"container", "inspect", "--size", "--", "container-owned", "container-foreign"}},
			output: `[
  {"Id":"container-owned","Name":"/switchyard-one","SizeRw":4096,"Config":{"Labels":` + labelsJSON(t, identity.Labels()) + `},"State":{"Status":"running","Running":true}},
  {"Id":"container-foreign","Name":"/colleague-service","SizeRw":8192,"Config":{"Labels":{"team":"local"}},"State":{"Status":"exited","Running":false}}
]`,
		},
		{
			command: Command{Executable: "docker-test", Arguments: []string{"volume", "ls", "--quiet"}},
			output:  "switchyard-volume\n",
		},
		{
			command: Command{Executable: "docker-test", Arguments: []string{"volume", "inspect", "--", "switchyard-volume"}},
			output:  `[{"Name":"switchyard-volume","Labels":` + labelsJSON(t, identity.Labels()) + `,"UsageData":{"Size":2048}}]`,
		},
		{
			command: Command{Executable: "docker-test", Arguments: []string{"network", "ls", "--quiet", "--no-trunc"}},
			output:  "foreign-network-id\n",
		},
		{
			command: Command{Executable: "docker-test", Arguments: []string{"network", "inspect", "--", "foreign-network-id"}},
			output:  `[{"Id":"foreign-network-id","Name":"foreign-network","Labels":{}}]`,
		},
	}}

	inventory, err := (DockerInventory{Runner: runner, DockerBinary: "docker-test"}).Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner.assertDone()
	if len(inventory.Resources) != 4 {
		t.Fatalf("resources: got %d, want 4", len(inventory.Resources))
	}
	if inventory.OwnedBytes != 6144 || inventory.ForeignBytes != 8192 {
		t.Fatalf("disk totals: owned=%d foreign=%d", inventory.OwnedBytes, inventory.ForeignBytes)
	}
	owned := 0
	foreign := 0
	for _, resource := range inventory.Resources {
		switch resource.Ownership {
		case OwnershipOwned:
			owned++
		case OwnershipForeign:
			foreign++
		}
	}
	if owned != 2 || foreign != 2 {
		t.Fatalf("ownership totals: owned=%d foreign=%d", owned, foreign)
	}
}

func TestNewInventoryDetectsDuplicateOwnedIdentitiesByKind(t *testing.T) {
	identity := testIdentity("duplicate")
	inventory, err := NewInventory([]Resource{
		ownedResource(ResourceContainer, "one", "one", identity, true),
		ownedResource(ResourceContainer, "two", "two", identity, false),
		ownedResource(ResourceVolume, "volume", "volume", identity, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Duplicates) != 1 || inventory.Duplicates[0].Kind != ResourceContainer {
		t.Fatalf("duplicates: %+v", inventory.Duplicates)
	}
}

func TestDockerInventoryRedactsRunnerErrors(t *testing.T) {
	secret := "docker-credential-must-not-appear"
	runner := &scriptedRunner{testing: t, runs: []scriptedRun{{
		command: Command{Executable: "docker", Arguments: []string{"container", "ls", "--all", "--quiet", "--no-trunc"}},
		err:     errors.New("failed with " + secret),
	}}}
	_, err := (DockerInventory{Runner: runner}).Inventory(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unredacted inventory error: %v", err)
	}
}

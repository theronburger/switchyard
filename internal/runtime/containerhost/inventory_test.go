package containerhost

import (
	"context"
	"errors"
	"slices"
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
  {"Id":"container-owned","Name":"/switchyard-one","SizeRw":4096,
   "Config":{"Image":"softwaremill/elasticmq-native:1.6.16","Labels":` + labelsJSON(t, identity.Labels()) + `},
   "HostConfig":{"PortBindings":{"9325/tcp":[{"HostIp":"127.0.0.1","HostPort":"19325"}],"9324/tcp":[{"HostIp":"127.0.0.1","HostPort":"19324"}]}},
   "NetworkSettings":{"Ports":{"9324/tcp":[{"HostIp":"127.0.0.1","HostPort":"19324"}],"9325/tcp":[{"HostIp":"127.0.0.1","HostPort":"19325"}]}},
   "State":{"Status":"running","Running":true}},
  {"Id":"container-foreign","Name":"/colleague-service","SizeRw":8192,"Config":{"Image":"colleague:dev","Labels":{"team":"local"}},"HostConfig":{"PortBindings":{}},"NetworkSettings":{"Ports":{}},"State":{"Status":"exited","Running":false}}
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
	var container Resource
	for _, resource := range inventory.Resources {
		if resource.ID == "container-owned" {
			container = resource
		}
	}
	wantBindings := []PortBinding{
		{Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP},
		{Host: LoopbackHostIPv4, HostPort: 19325, ContainerPort: 9325, Protocol: PortProtocolTCP},
	}
	if container.Image != "softwaremill/elasticmq-native:1.6.16" ||
		!slices.Equal(container.PortBindings, wantBindings) ||
		!slices.Equal(container.PublishedPortBindings, wantBindings) {
		t.Fatalf("container immutable configuration: %+v", container)
	}
}

func TestInventoryRevisionIncludesContainerImageAndPortBindings(t *testing.T) {
	identity := testIdentity("revision")
	base := ownedResource(ResourceContainer, "container", "container", identity, true)
	base.PortBindings = []PortBinding{{
		Host: LoopbackHostIPv4, HostPort: 19324, ContainerPort: 9324, Protocol: PortProtocolTCP,
	}}
	base.PublishedPortBindings = clonePortBindings(base.PortBindings)
	first, err := NewInventory([]Resource{base})
	if err != nil {
		t.Fatal(err)
	}

	changedImage := base
	changedImage.Image = "elasticmq:next"
	second, err := NewInventory([]Resource{changedImage})
	if err != nil {
		t.Fatal(err)
	}
	changedPort := base
	changedPort.PortBindings = clonePortBindings(base.PortBindings)
	changedPort.PublishedPortBindings = clonePortBindings(base.PublishedPortBindings)
	changedPort.PortBindings[0].HostPort++
	third, err := NewInventory([]Resource{changedPort})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision || first.Revision == third.Revision {
		t.Fatal("container immutable configuration did not affect inventory revision")
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

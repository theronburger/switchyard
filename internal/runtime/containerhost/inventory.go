package containerhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type ResourceReader interface {
	Inventory(context.Context) (Inventory, error)
	Inspect(context.Context, ResourceKind, string) (Resource, error)
}

type DockerInventory struct {
	Runner       Runner
	DockerBinary string
}

func (docker DockerInventory) Inventory(ctx context.Context) (Inventory, error) {
	if docker.Runner == nil {
		return Inventory{}, errors.New("docker inventory runner is required")
	}
	resources := make([]Resource, 0)
	for _, kind := range []ResourceKind{ResourceContainer, ResourceVolume, ResourceNetwork} {
		if err := ctx.Err(); err != nil {
			return Inventory{}, err
		}
		references, err := docker.list(ctx, kind)
		if err != nil {
			return Inventory{}, err
		}
		if len(references) == 0 {
			continue
		}
		inspected, err := docker.inspect(ctx, kind, references)
		if err != nil {
			return Inventory{}, err
		}
		if len(inspected) != len(references) {
			return Inventory{}, errors.New("docker inventory changed during inspection")
		}
		resources = append(resources, inspected...)
	}
	return NewInventory(resources)
}

func (docker DockerInventory) Inspect(
	ctx context.Context,
	kind ResourceKind,
	reference string,
) (Resource, error) {
	if docker.Runner == nil {
		return Resource{}, errors.New("docker inventory runner is required")
	}
	if !kind.Valid() || !safeReference(reference) {
		return Resource{}, errors.New("docker resource reference is invalid")
	}
	resources, err := docker.inspect(ctx, kind, []string{reference})
	if err != nil {
		return Resource{}, err
	}
	if len(resources) != 1 {
		return Resource{}, errors.New("docker inspect did not return exactly one resource")
	}
	inventory, err := NewInventory(resources)
	if err != nil {
		return Resource{}, err
	}
	return inventory.Resources[0], nil
}

func (docker DockerInventory) list(ctx context.Context, kind ResourceKind) ([]string, error) {
	command := Command{
		Executable: docker.executable(),
		Arguments:  listArguments(kind),
	}
	output, err := docker.Runner.Run(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("list docker %s resources: %w", kind, redactRunnerError(command, err))
	}
	return parseReferences(output)
}

func (docker DockerInventory) inspect(
	ctx context.Context,
	kind ResourceKind,
	references []string,
) ([]Resource, error) {
	arguments := []string{string(kind), "inspect"}
	if kind == ResourceContainer {
		arguments = append(arguments, "--size")
	}
	arguments = append(arguments, "--")
	arguments = append(arguments, references...)
	command := Command{Executable: docker.executable(), Arguments: arguments}
	output, err := docker.Runner.Run(ctx, command)
	if err != nil {
		return nil, fmt.Errorf("inspect docker %s resources: %w", kind, redactRunnerError(command, err))
	}
	return decodeInspectedResources(kind, output)
}

func (docker DockerInventory) executable() string {
	if docker.DockerBinary == "" {
		return "docker"
	}
	return docker.DockerBinary
}

func listArguments(kind ResourceKind) []string {
	switch kind {
	case ResourceContainer:
		return []string{"container", "ls", "--all", "--quiet", "--no-trunc"}
	case ResourceVolume:
		return []string{"volume", "ls", "--quiet"}
	case ResourceNetwork:
		return []string{"network", "ls", "--quiet", "--no-trunc"}
	default:
		return []string{string(kind), "ls", "--quiet"}
	}
}

func parseReferences(contents []byte) ([]string, error) {
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(lines))
	references := make([]string, 0, len(lines))
	for _, line := range lines {
		reference := strings.TrimSpace(line)
		if !safeReference(reference) {
			return nil, errors.New("docker returned an invalid resource reference")
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, errors.New("docker returned a duplicate resource reference")
		}
		seen[reference] = struct{}{}
		references = append(references, reference)
	}
	return references, nil
}

func safeReference(reference string) bool {
	return reference != "" && len(reference) <= 255 && !strings.ContainsAny(reference, "\x00\r\n")
}

func decodeInspectedResources(kind ResourceKind, contents []byte) ([]Resource, error) {
	switch kind {
	case ResourceContainer:
		var inspected []struct {
			ID     string `json:"Id"`
			Name   string `json:"Name"`
			SizeRW int64  `json:"SizeRw"`
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
			State struct {
				Status  string `json:"Status"`
				Running bool   `json:"Running"`
			} `json:"State"`
		}
		if err := json.Unmarshal(contents, &inspected); err != nil {
			return nil, errors.New("docker returned invalid container inspection JSON")
		}
		resources := make([]Resource, 0, len(inspected))
		for _, item := range inspected {
			resources = append(resources, Resource{
				Kind:      kind,
				ID:        item.ID,
				Name:      strings.TrimPrefix(item.Name, "/"),
				State:     item.State.Status,
				Running:   item.State.Running,
				SizeBytes: item.SizeRW,
				Labels:    item.Config.Labels,
			})
		}
		return resources, nil
	case ResourceVolume:
		var inspected []struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
			Usage  struct {
				Size int64 `json:"Size"`
			} `json:"UsageData"`
		}
		if err := json.Unmarshal(contents, &inspected); err != nil {
			return nil, errors.New("docker returned invalid volume inspection JSON")
		}
		resources := make([]Resource, 0, len(inspected))
		for _, item := range inspected {
			resources = append(resources, Resource{
				Kind: kind, ID: item.Name, Name: item.Name, State: "available",
				SizeBytes: item.Usage.Size, Labels: item.Labels,
			})
		}
		return resources, nil
	case ResourceNetwork:
		var inspected []struct {
			ID     string            `json:"Id"`
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
		}
		if err := json.Unmarshal(contents, &inspected); err != nil {
			return nil, errors.New("docker returned invalid network inspection JSON")
		}
		resources := make([]Resource, 0, len(inspected))
		for _, item := range inspected {
			resources = append(resources, Resource{
				Kind: kind, ID: item.ID, Name: item.Name, State: "available", Labels: item.Labels,
			})
		}
		return resources, nil
	default:
		return nil, errors.New("docker resource kind is invalid")
	}
}

func NewInventory(resources []Resource) (Inventory, error) {
	ownedByIdentity := make(map[resourceIdentityKey][]string)
	normalized := make([]Resource, 0, len(resources))
	var ownedBytes int64
	var foreignBytes int64
	for _, resource := range resources {
		if !resource.Kind.Valid() || !safeReference(resource.ID) || !safeReference(resource.Name) {
			return Inventory{}, errors.New("docker inventory contains an invalid resource")
		}
		resource.Labels = cloneLabels(resource.Labels)
		resource.Ownership, resource.Identity = ClassifyLabels(resource.Labels)
		if resource.SizeBytes < 0 {
			resource.SizeBytes = 0
		}
		if resource.Ownership == OwnershipOwned {
			ownedBytes = saturatingAdd(ownedBytes, resource.SizeBytes)
			key := resourceIdentityKey{Kind: resource.Kind, Identity: resource.Identity}
			ownedByIdentity[key] = append(ownedByIdentity[key], resource.ID)
		} else {
			foreignBytes = saturatingAdd(foreignBytes, resource.SizeBytes)
		}
		normalized = append(normalized, resource)
	}
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].Kind != normalized[right].Kind {
			return normalized[left].Kind < normalized[right].Kind
		}
		return normalized[left].ID < normalized[right].ID
	})

	duplicates := make([]DuplicateIdentity, 0)
	for key, resourceIDs := range ownedByIdentity {
		if len(resourceIDs) < 2 {
			continue
		}
		sort.Strings(resourceIDs)
		duplicates = append(duplicates, DuplicateIdentity{
			Kind: key.Kind, Identity: key.Identity, ResourceIDs: resourceIDs,
		})
	}
	sort.Slice(duplicates, func(left, right int) bool {
		if duplicates[left].Kind != duplicates[right].Kind {
			return duplicates[left].Kind < duplicates[right].Kind
		}
		return identitySortKey(duplicates[left].Identity) < identitySortKey(duplicates[right].Identity)
	})

	inventory := Inventory{
		Resources: normalized, Duplicates: duplicates, OwnedBytes: ownedBytes, ForeignBytes: foreignBytes,
	}
	inventory.Revision = inventoryRevision(inventory)
	return inventory, nil
}

type resourceIdentityKey struct {
	Kind     ResourceKind
	Identity Identity
}

func inventoryRevision(inventory Inventory) string {
	contents, _ := json.Marshal(struct {
		Resources  []Resource
		Duplicates []DuplicateIdentity
	}{Resources: inventory.Resources, Duplicates: inventory.Duplicates})
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func identitySortKey(identity Identity) string {
	return identity.EnvironmentID + "\x00" + identity.ServiceID + "\x00" + identity.RunID + "\x00" + identity.InstanceID
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func saturatingAdd(total, value int64) int64 {
	const maximumInt64 = int64(^uint64(0) >> 1)
	if value > maximumInt64-total {
		return maximumInt64
	}
	return total + value
}

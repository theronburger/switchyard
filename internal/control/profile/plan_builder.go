package profile

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

const artifactProjectionID = "private-artifacts-v1"

type PlanBuilder struct {
	registry Registry
}

func NewPlanBuilder(registry Registry) PlanBuilder {
	return PlanBuilder{registry: registry}
}

func (builder PlanBuilder) Build(request environmentcontrol.PlanningRequest) (environmentcontrol.ExecutionPlan, error) {
	registration, err := builder.registry.Lookup(request.EnvironmentID)
	if err != nil || request.Intent.Adapter != registration.ProfileDigest || request.RunID == "" {
		return environmentcontrol.ExecutionPlan{}, ErrProfileInvalid
	}
	profile := registration.Profile
	targetID := request.Intent.TargetID
	if targetID == "" {
		targetID = profile.DefaultTarget
	}
	target, found := profile.Targets[targetID]
	if !found {
		return environmentcontrol.ExecutionPlan{}, ErrProfileInvalid
	}
	serviceIDs, err := orderedServices(profile, request.Intent.ServiceIDs)
	if err != nil {
		return environmentcontrol.ExecutionPlan{}, err
	}
	leases := make(map[portlease.Key]portlease.Lease, len(request.AssignedPorts))
	for _, lease := range request.AssignedPorts {
		leases[lease.Key] = lease
	}
	runRoot := filepath.Join(registration.RuntimeRoot, "repositories", registration.ProfileKey, registration.WorktreeID,
		"environments", registration.EnvironmentID, "runs", request.RunID)
	plan := environmentcontrol.ExecutionPlan{}
	artifactSet := make(map[string]struct{})
	infrastructureSet := make(map[string]struct{})
	routeEnvironment := make(map[string]configuration.ValueRef)
	for _, routeServiceID := range serviceIDs {
		for purpose, port := range profile.Services[routeServiceID].Ports {
			for _, published := range port.Publish {
				if _, duplicate := routeEnvironment[published.Name]; duplicate {
					return environmentcontrol.ExecutionPlan{}, ErrProfileInvalid
				}
				routeEnvironment[published.Name] = configuration.ValueRef{URL: &configuration.URLReference{
					Service: routeServiceID, Purpose: purpose, Scheme: published.Scheme, Host: published.Host, Path: published.Path,
				}}
			}
		}
	}
	for _, serviceID := range serviceIDs {
		service := profile.Services[serviceID]
		resolvedEnvironment, err := resolveEnvironment(registration, runRoot, serviceID, target.Environment, leases, routeEnvironment, target.Environment, service.Environment)
		if err != nil {
			return environmentcontrol.ExecutionPlan{}, err
		}
		for index, command := range service.Prepare {
			spec, err := finiteCommand(registration, runRoot, request.RunID, serviceID, "prepare-"+strconv.Itoa(index), command, target.Environment, resolvedEnvironment, leases)
			if err != nil {
				return environmentcontrol.ExecutionPlan{}, err
			}
			plan.Preparations = append(plan.Preparations, spec)
		}
		for index, command := range service.Initialize {
			spec, err := finiteCommand(registration, runRoot, request.RunID, serviceID, "initialize-"+strconv.Itoa(index), command, target.Environment, resolvedEnvironment, leases)
			if err != nil {
				return environmentcontrol.ExecutionPlan{}, err
			}
			plan.Initializations = append(plan.Initializations, spec)
		}
		arguments, err := resolveValues(registration, runRoot, serviceID, service.Command.Arguments, target.Environment, leases)
		if err != nil {
			return environmentcontrol.ExecutionPlan{}, err
		}
		commandEnvironment, err := resolveEnvironment(registration, runRoot, serviceID, target.Environment, leases, service.Command.Environment)
		if err != nil {
			return environmentcontrol.ExecutionPlan{}, err
		}
		resolvedEnvironment = mergeEnvironment(resolvedEnvironment, commandEnvironment)
		portKeys := make([]portlease.Key, 0, len(service.Ports))
		for purpose := range service.Ports {
			key := portlease.Key{EnvironmentID: request.EnvironmentID, ServiceID: serviceID, Purpose: purpose}
			if _, found := leases[key]; !found {
				return environmentcontrol.ExecutionPlan{}, ErrProfileInvalid
			}
			portKeys = append(portKeys, key)
		}
		sort.Slice(portKeys, func(left, right int) bool { return portKeys[left].Purpose < portKeys[right].Purpose })
		plan.Services = append(plan.Services, environmentcontrol.ServiceLaunch{
			ID: serviceID,
			Process: processhost.LaunchSpec{
				EnvironmentID: request.EnvironmentID, ServiceID: serviceID, RunID: request.RunID,
				Executable: service.Command.Executable, Arguments: arguments, Environment: resolvedEnvironment,
				Directory:    filepath.Join(registration.WorktreeRoot, service.Command.WorkingDirectory),
				RunDirectory: filepath.Join(runRoot, "services", serviceID),
			},
			PortKeys: portKeys, Readiness: environmentcontrol.ReadinessSpec{ID: readinessID(serviceID)},
		})
		for _, artifactID := range service.Artifacts {
			artifactSet[artifactID] = struct{}{}
		}
		for _, infrastructureID := range service.Infrastructure {
			infrastructureSet[infrastructureID] = struct{}{}
		}
	}
	if len(artifactSet) != 0 {
		ids := sortedSet(artifactSet)
		plan.Projection = &environmentcontrol.ProjectionRequest{ID: artifactProjectionID, ArtifactIDs: ids}
	}
	for _, infrastructureID := range sortedSet(infrastructureSet) {
		definition := profile.Infrastructure[infrastructureID]
		goal := containerhost.Goal{
			Kind:  containerhost.ResourceContainer,
			Name:  "switchyard-" + shortIdentity(request.EnvironmentID) + "-" + infrastructureID,
			Image: definition.Image, DesiredState: containerhost.DesiredRunning,
			Identity: containerhost.Identity{
				EnvironmentID: request.EnvironmentID, ServiceID: infrastructureID,
				RunID: request.RunID, InstanceID: registration.DaemonInstanceID,
			},
		}
		for _, bindingID := range sortedContainerPortKeys(definition.ContainerPorts) {
			binding := definition.ContainerPorts[bindingID]
			key := portlease.Key{EnvironmentID: request.EnvironmentID, ServiceID: binding.Service, Purpose: binding.Purpose}
			lease, found := leases[key]
			if !found {
				return environmentcontrol.ExecutionPlan{}, ErrProfileInvalid
			}
			goal.PortBindings = append(goal.PortBindings, containerhost.PortBinding{
				Host: containerhost.LoopbackHostIPv4, HostPort: lease.Port,
				ContainerPort: binding.ContainerPort, Protocol: containerhost.PortProtocolTCP,
			})
		}
		plan.Infrastructure = append(plan.Infrastructure, goal)
	}
	stages, err := stagedServices(profile, plan.Services)
	if err != nil {
		return environmentcontrol.ExecutionPlan{}, err
	}
	plan.ServiceStages = stages
	plan.Services = nil
	return plan, nil
}

func stagedServices(profile configuration.Repository, launches []environmentcontrol.ServiceLaunch) ([][]environmentcontrol.ServiceLaunch, error) {
	selected := make(map[string]environmentcontrol.ServiceLaunch, len(launches))
	for _, launch := range launches {
		selected[launch.ID] = launch
	}
	depths := make(map[string]int, len(launches))
	var depth func(string) (int, error)
	depth = func(id string) (int, error) {
		if existing, found := depths[id]; found {
			return existing, nil
		}
		service, found := profile.Services[id]
		if !found {
			return 0, ErrProfileInvalid
		}
		result := 0
		for _, dependency := range service.Dependencies {
			if _, included := selected[dependency]; !included {
				return 0, ErrProfileInvalid
			}
			dependencyDepth, err := depth(dependency)
			if err != nil {
				return 0, err
			}
			if dependencyDepth+1 > result {
				result = dependencyDepth + 1
			}
		}
		depths[id] = result
		return result, nil
	}
	stages := make([][]environmentcontrol.ServiceLaunch, 0)
	for _, launch := range launches {
		stage, err := depth(launch.ID)
		if err != nil {
			return nil, err
		}
		for len(stages) <= stage {
			stages = append(stages, nil)
		}
		stages[stage] = append(stages[stage], launch)
	}
	for _, stage := range stages {
		sort.Slice(stage, func(left, right int) bool { return stage[left].ID < stage[right].ID })
	}
	return stages, nil
}

func orderedServices(profile configuration.Repository, selected []string) ([]string, error) {
	requested := make(map[string]struct{}, len(selected))
	var add func(string) error
	add = func(id string) error {
		service, found := profile.Services[id]
		if !found || !service.IsAvailable() {
			return ErrProfileInvalid
		}
		if _, exists := requested[id]; exists {
			return nil
		}
		for _, dependency := range service.Dependencies {
			if err := add(dependency); err != nil {
				return err
			}
		}
		requested[id] = struct{}{}
		return nil
	}
	for _, id := range selected {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	ordered := make([]string, 0, len(requested))
	visited := make(map[string]bool, len(requested))
	var visit func(string)
	visit = func(id string) {
		if visited[id] {
			return
		}
		for _, dependency := range profile.Services[id].Dependencies {
			if _, included := requested[dependency]; included {
				visit(dependency)
			}
		}
		visited[id] = true
		ordered = append(ordered, id)
	}
	ids := sortedSet(requested)
	for _, id := range ids {
		visit(id)
	}
	return ordered, nil
}

func finiteCommand(registration Registration, runRoot, runID, serviceID, id string, command configuration.Command, targets map[string]configuration.ValueRef, baseEnvironment []string, leases map[portlease.Key]portlease.Lease) (environmentcontrol.PreparationSpec, error) {
	arguments, err := resolveValues(registration, runRoot, serviceID, command.Arguments, targets, leases)
	if err != nil {
		return environmentcontrol.PreparationSpec{}, err
	}
	environment, err := resolveEnvironment(registration, runRoot, serviceID, targets, leases, command.Environment)
	if err != nil {
		return environmentcontrol.PreparationSpec{}, err
	}
	duration, err := time.ParseDuration(command.Timeout)
	if err != nil {
		return environmentcontrol.PreparationSpec{}, err
	}
	return environmentcontrol.PreparationSpec{
		ID: serviceID + "." + id, ServiceID: serviceID,
		LogReference: filepath.ToSlash(filepath.Join(runID, "preparations", serviceID, id)),
		Executable:   command.Executable, Arguments: arguments,
		Environment:  mergeEnvironment(baseEnvironment, environment),
		Directory:    filepath.Join(registration.WorktreeRoot, command.WorkingDirectory),
		RunDirectory: filepath.Join(runRoot, "preparations", serviceID, id), Timeout: duration,
	}, nil
}

func resolveEnvironment(registration Registration, runRoot, serviceID string, targets map[string]configuration.ValueRef, leases map[portlease.Key]portlease.Lease, layers ...map[string]configuration.ValueRef) ([]string, error) {
	values := map[string]string{
		"HOME": registration.HomeDirectory, "PATH": registration.ExecutablePath, "TMPDIR": registration.TemporaryDirectory,
	}
	for _, layer := range layers {
		keys := make([]string, 0, len(layer))
		for name := range layer {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			value, err := resolveValue(registration, runRoot, serviceID, layer[name], targets, leases)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+values[name])
	}
	return result, nil
}

func resolveValues(registration Registration, runRoot, serviceID string, values []configuration.ValueRef, targets map[string]configuration.ValueRef, leases map[portlease.Key]portlease.Lease) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		resolved, err := resolveValue(registration, runRoot, serviceID, value, targets, leases)
		if err != nil {
			return nil, err
		}
		result = append(result, resolved)
	}
	return result, nil
}

func resolveValue(registration Registration, runRoot, serviceID string, value configuration.ValueRef, targets map[string]configuration.ValueRef, leases map[portlease.Key]portlease.Lease) (string, error) {
	switch {
	case value.Literal != nil:
		return *value.Literal, nil
	case value.Target != "":
		target, found := targets[value.Target]
		if !found || target.Target != "" {
			return "", ErrProfileInvalid
		}
		return resolveValue(registration, runRoot, serviceID, target, nil, leases)
	case value.Port != nil:
		portService := value.Port.Service
		if portService == "" {
			portService = serviceID
		}
		lease, found := leases[portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: portService, Purpose: value.Port.Purpose}]
		if !found {
			return "", ErrProfileInvalid
		}
		return strconv.Itoa(lease.Port), nil
	case value.URL != nil:
		portService := value.URL.Service
		if portService == "" {
			portService = serviceID
		}
		lease, found := leases[portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: portService, Purpose: value.URL.Purpose}]
		if !found {
			return "", ErrProfileInvalid
		}
		host := lease.Host
		if value.URL.Host == "localhost" {
			host = "localhost"
		}
		return value.URL.Scheme + "://" + host + ":" + strconv.Itoa(lease.Port) + value.URL.Path, nil
	case value.WorktreePath != nil:
		return filepath.Join(registration.WorktreeRoot, *value.WorktreePath), nil
	case value.RuntimePath != nil:
		return filepath.Join(runRoot, *value.RuntimePath), nil
	case value.Artifact != "":
		artifact, found := registration.Profile.Artifacts[value.Artifact]
		if !found {
			return "", ErrProfileInvalid
		}
		filename := artifact.Filename
		if filename == "" {
			filename = value.Artifact
		}
		return filepath.Join(runRoot, "artifacts", filename), nil
	case value.Cache != "":
		cache := registration.Profile.Caches[value.Cache]
		directory := cache.Directory
		if directory == "" {
			directory = value.Cache
		}
		return filepath.Join(registration.CacheRoot, directory), nil
	case value.Value != "":
		resolved, found := registration.Values[value.Value]
		if !found {
			return "", ErrProfileInvalid
		}
		return resolved, nil
	default:
		return "", ErrProfileInvalid
	}
}

func mergeEnvironment(layers ...[]string) []string {
	values := make(map[string]string)
	for _, layer := range layers {
		for _, entry := range layer {
			name, value, found := strings.Cut(entry, "=")
			if found {
				values[name] = value
			}
		}
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+values[name])
	}
	return result
}

func readinessID(serviceID string) string { return "profile-readiness:" + serviceID }

func sortedSet[T ~string](values map[T]struct{}) []T {
	result := make([]T, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func sortedContainerPortKeys(values map[string]configuration.ContainerPort) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func shortIdentity(value string) string {
	if len(value) > 24 {
		return value[:24]
	}
	return value
}

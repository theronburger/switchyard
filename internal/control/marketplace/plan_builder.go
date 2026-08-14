package marketplacecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

const (
	marketplaceAdapterID            = "marketplace"
	marketplaceServerlessProjection = "marketplace.serverless.nonprofit-service.v1"
	readinessIDPrefix               = "marketplace.readiness."
	readinessIDSuffix               = ".v1"
)

var ErrMarketplacePlanInvalid = errors.New("Marketplace execution plan is invalid")

type ExecutionCatalog interface {
	PlanEnvironment([]string, map[string]map[string]int) ([]marketplaceadapter.ServicePlan, error)
	Definition(string) (marketplaceadapter.ServiceDefinition, bool)
}

type PlanBuilder struct {
	registry EnvironmentRegistry
	catalog  ExecutionCatalog
}

func NewPlanBuilder(registry EnvironmentRegistry, catalog ExecutionCatalog) (PlanBuilder, error) {
	if len(registry.byEnvironment) == 0 || catalog == nil {
		return PlanBuilder{}, ErrMarketplacePlanInvalid
	}
	return PlanBuilder{registry: registry, catalog: catalog}, nil
}

func NewDefaultPlanBuilder(registry EnvironmentRegistry) (PlanBuilder, error) {
	return NewPlanBuilder(registry, marketplaceadapter.DefaultCatalog())
}

func (builder PlanBuilder) Build(request environment.PlanningRequest) (environment.ExecutionPlan, error) {
	registration, err := builder.registry.Lookup(request.EnvironmentID)
	if err != nil || request.Intent.Adapter != marketplaceAdapterID ||
		!registryIDPattern.MatchString(request.RunID) || len(request.Intent.ServiceIDs) == 0 {
		return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
	}
	serviceIDs := append([]string(nil), request.Intent.ServiceIDs...)
	sort.Strings(serviceIDs)
	definitions, err := builder.definitions(serviceIDs)
	if err != nil {
		return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
	}
	assignedPorts, err := assignedPortsForCatalog(request, definitions)
	if err != nil {
		return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
	}
	servicePlans, err := builder.catalog.PlanEnvironment(serviceIDs, assignedPorts)
	if err != nil || len(servicePlans) != len(serviceIDs) {
		return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
	}
	servicePlans = append([]marketplaceadapter.ServicePlan(nil), servicePlans...)
	sort.Slice(servicePlans, func(left, right int) bool {
		return servicePlans[left].ID < servicePlans[right].ID
	})

	plan := environment.ExecutionPlan{}
	seenServices := make(map[string]struct{}, len(servicePlans))
	for _, servicePlan := range servicePlans {
		if _, duplicate := seenServices[servicePlan.ID]; duplicate {
			return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
		}
		seenServices[servicePlan.ID] = struct{}{}
		definition, found := definitions[servicePlan.ID]
		if !found {
			return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
		}
		launch, err := buildServiceLaunch(registration, request.RunID, definition, servicePlan)
		if err != nil {
			return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
		}
		plan.Services = append(plan.Services, launch)
		goals, err := buildInfrastructureGoals(registration, request.RunID, servicePlan)
		if err != nil {
			return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
		}
		plan.Infrastructure = append(plan.Infrastructure, goals...)
		if servicePlan.ServerlessOverlay != nil {
			if plan.Projection != nil || servicePlan.ID != "nonprofit-service" {
				return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
			}
			plan.Projection = &environment.ProjectionRequest{ID: marketplaceServerlessProjection}
		}
	}
	return plan, nil
}

func (builder PlanBuilder) definitions(
	serviceIDs []string,
) (map[string]marketplaceadapter.ServiceDefinition, error) {
	definitions := make(map[string]marketplaceadapter.ServiceDefinition, len(serviceIDs))
	for _, serviceID := range serviceIDs {
		if _, duplicate := definitions[serviceID]; duplicate {
			return nil, ErrMarketplacePlanInvalid
		}
		definition, found := builder.catalog.Definition(serviceID)
		if !found || definition.ID != serviceID {
			return nil, ErrMarketplacePlanInvalid
		}
		definitions[serviceID] = definition
	}
	return definitions, nil
}

func assignedPortsForCatalog(
	request environment.PlanningRequest,
	definitions map[string]marketplaceadapter.ServiceDefinition,
) (map[string]map[string]int, error) {
	result := make(map[string]map[string]int, len(definitions))
	purposeToRequirement := make(map[string]map[string]string, len(definitions))
	for serviceID, definition := range definitions {
		result[serviceID] = make(map[string]int, len(definition.PortRequirements))
		purposeToRequirement[serviceID] = make(map[string]string, len(definition.PortRequirements))
		for _, requirement := range definition.PortRequirements {
			if requirement.Purpose == "" {
				return nil, ErrMarketplacePlanInvalid
			}
			if _, duplicate := purposeToRequirement[serviceID][requirement.Purpose]; duplicate {
				return nil, ErrMarketplacePlanInvalid
			}
			purposeToRequirement[serviceID][requirement.Purpose] = requirement.ID
		}
	}
	seen := make(map[portlease.Key]struct{}, len(request.AssignedPorts))
	seenPorts := make(map[int]struct{}, len(request.AssignedPorts))
	for _, lease := range request.AssignedPorts {
		if lease.Key.EnvironmentID != request.EnvironmentID || lease.Host != "127.0.0.1" ||
			lease.Port < 1 || lease.Port > 65535 {
			return nil, ErrMarketplacePlanInvalid
		}
		if _, duplicate := seen[lease.Key]; duplicate {
			return nil, ErrMarketplacePlanInvalid
		}
		if _, duplicate := seenPorts[lease.Port]; duplicate {
			return nil, ErrMarketplacePlanInvalid
		}
		seen[lease.Key] = struct{}{}
		seenPorts[lease.Port] = struct{}{}
		requirementID, found := purposeToRequirement[lease.Key.ServiceID][lease.Key.Purpose]
		if !found {
			return nil, ErrMarketplacePlanInvalid
		}
		result[lease.Key.ServiceID][requirementID] = lease.Port
	}
	return result, nil
}

func buildServiceLaunch(
	registration EnvironmentRegistration,
	runID string,
	definition marketplaceadapter.ServiceDefinition,
	plan marketplaceadapter.ServicePlan,
) (environment.ServiceLaunch, error) {
	if plan.ID != definition.ID || plan.RunCommand.Executable != marketplaceadapter.RepositoryYarnExecutable ||
		!safeRelativeDirectory(plan.RunCommand.WorkingDirectory) {
		return environment.ServiceLaunch{}, ErrMarketplacePlanInvalid
	}
	arguments := make([]string, 0, len(plan.RunCommand.Arguments)+1)
	arguments = append(arguments, registration.YarnCJS)
	for _, argument := range plan.RunCommand.Arguments {
		if argument == "" || strings.ContainsRune(argument, 0) {
			return environment.ServiceLaunch{}, ErrMarketplacePlanInvalid
		}
		arguments = append(arguments, argument)
	}
	environmentVariables := []string{
		"HOME=" + registration.HomeDirectory,
		"PATH=" + registration.ExecutablePath,
		"TMPDIR=" + registration.TemporaryDirectory,
	}
	seenEnvironmentVariables := map[string]struct{}{
		"HOME": {}, "PATH": {}, "TMPDIR": {},
	}
	for _, variable := range plan.Environment {
		if variable.Name == "" || strings.ContainsAny(variable.Name, "=\x00") ||
			strings.ContainsRune(variable.Value, 0) {
			return environment.ServiceLaunch{}, ErrMarketplacePlanInvalid
		}
		if _, duplicate := seenEnvironmentVariables[variable.Name]; duplicate {
			return environment.ServiceLaunch{}, ErrMarketplacePlanInvalid
		}
		seenEnvironmentVariables[variable.Name] = struct{}{}
		environmentVariables = append(environmentVariables, variable.Name+"="+variable.Value)
	}

	infrastructurePorts := make(map[string]struct{})
	for _, infrastructure := range plan.Infrastructure {
		for _, binding := range infrastructure.Ports {
			infrastructurePorts[binding.PortRequirement] = struct{}{}
		}
	}
	portKeys := make([]portlease.Key, 0, len(plan.Ports))
	for _, assignment := range plan.Ports {
		if _, infrastructureOwned := infrastructurePorts[assignment.RequirementID]; infrastructureOwned {
			continue
		}
		purpose := ""
		for _, requirement := range definition.PortRequirements {
			if requirement.ID == assignment.RequirementID {
				purpose = requirement.Purpose
				break
			}
		}
		if purpose == "" {
			return environment.ServiceLaunch{}, ErrMarketplacePlanInvalid
		}
		portKeys = append(portKeys, portlease.Key{
			EnvironmentID: registration.EnvironmentID,
			ServiceID:     plan.ID,
			Purpose:       purpose,
		})
	}
	return environment.ServiceLaunch{
		ID: plan.ID,
		Process: processhost.LaunchSpec{
			EnvironmentID: registration.EnvironmentID,
			ServiceID:     plan.ID,
			RunID:         runID,
			Executable:    registration.NodeExecutable,
			Arguments:     arguments,
			Environment:   environmentVariables,
			Directory:     filepath.Join(registration.WorktreeRoot, plan.RunCommand.WorkingDirectory),
			RunDirectory: filepath.Join(
				registration.RunRoot,
				"environments",
				registration.EnvironmentID,
				"runs",
				runID,
				"services",
				plan.ID,
			),
		},
		PortKeys:  portKeys,
		Readiness: environment.ReadinessSpec{ID: readinessID(plan.ID)},
	}, nil
}

func buildInfrastructureGoals(
	registration EnvironmentRegistration,
	runID string,
	servicePlan marketplaceadapter.ServicePlan,
) ([]containerhost.Goal, error) {
	assignments := make(map[string]marketplaceadapter.PortAssignment, len(servicePlan.Ports))
	for _, assignment := range servicePlan.Ports {
		if _, duplicate := assignments[assignment.RequirementID]; duplicate {
			return nil, ErrMarketplacePlanInvalid
		}
		assignments[assignment.RequirementID] = assignment
	}
	goals := make([]containerhost.Goal, 0, len(servicePlan.Infrastructure))
	for _, infrastructure := range servicePlan.Infrastructure {
		if infrastructure.Kind != "container" || !infrastructure.Dedicated ||
			infrastructure.Scope != marketplaceadapter.EnvironmentInfrastructureScope {
			return nil, ErrMarketplacePlanInvalid
		}
		identity := containerhost.Identity{
			EnvironmentID: registration.EnvironmentID,
			ServiceID:     servicePlan.ID + "." + infrastructure.ID,
			RunID:         runID,
			InstanceID:    registration.DaemonInstanceID,
		}
		if identity.Validate() != nil {
			return nil, ErrMarketplacePlanInvalid
		}
		portBindings := make([]containerhost.PortBinding, 0, len(infrastructure.Ports))
		seenPortRequirements := make(map[string]struct{}, len(infrastructure.Ports))
		for _, infrastructurePort := range infrastructure.Ports {
			assignment, found := assignments[infrastructurePort.PortRequirement]
			if !found || assignment.Host != containerhost.LoopbackHostIPv4 ||
				infrastructurePort.ContainerPort < 1 || infrastructurePort.ContainerPort > 65535 {
				return nil, ErrMarketplacePlanInvalid
			}
			if _, duplicate := seenPortRequirements[infrastructurePort.PortRequirement]; duplicate {
				return nil, ErrMarketplacePlanInvalid
			}
			seenPortRequirements[infrastructurePort.PortRequirement] = struct{}{}
			portBindings = append(portBindings, containerhost.PortBinding{
				Host:          containerhost.LoopbackHostIPv4,
				HostPort:      assignment.Port,
				ContainerPort: infrastructurePort.ContainerPort,
				Protocol:      containerhost.PortProtocolTCP,
			})
		}
		goals = append(goals, containerhost.Goal{
			Kind:         containerhost.ResourceContainer,
			Name:         infrastructureName(registration.EnvironmentID, runID, infrastructure.ID),
			Image:        infrastructure.Image,
			PortBindings: portBindings,
			Identity:     identity,
			DesiredState: containerhost.DesiredRunning,
		})
	}
	return goals, nil
}

func readinessID(serviceID string) string {
	return readinessIDPrefix + serviceID + readinessIDSuffix
}

func infrastructureName(environmentID, runID, infrastructureID string) string {
	digest := sha256.Sum256([]byte(environmentID + "\x00" + runID + "\x00" + infrastructureID))
	return "switchyard-" + infrastructureID + "-" + hex.EncodeToString(digest[:8])
}

func safeRelativeDirectory(directory string) bool {
	return directory != "" && !filepath.IsAbs(directory) && filepath.Clean(directory) == directory &&
		directory != ".." && !strings.HasPrefix(directory, ".."+string(filepath.Separator)) &&
		!strings.ContainsRune(directory, 0)
}

package marketplacecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

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
	marketplacePreparationTimeout   = 10 * time.Minute
	marketplaceCurlExecutable       = "/usr/bin/curl"
	elasticMQReadinessTimeout       = 50 * time.Second
	elasticMQCreateQueueTimeout     = 10 * time.Second
)

var (
	ErrMarketplacePlanInvalid = errors.New("Marketplace execution plan is invalid")
	elasticMQQueueNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	elasticMQQueueNames       = []string{
		"local-nonprofit-service-chapter-request-queue",
		"local-nonprofit-service-chapter-request-dlq",
		"local-nonprofit-service-chapter-request-finalize-queue",
		"local-nonprofit-service-chapter-request-finalize-dlq",
		"local-nonprofit-service-organizer-verification-queue",
		"local-nonprofit-service-organizer-verification-dlq",
		"local-nonprofit-service-process-requests",
		"local-nonprofit-service-process-requests-dlq",
		"local-nonprofit-service-chapter-request",
		"local-nonprofit-service-chapter-request-finalize",
		"local-nonprofit-service-organizer-verification",
	}
)

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
		preparations, err := buildPreparations(registration, request.RunID, servicePlan)
		if err != nil {
			return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
		}
		plan.Preparations = append(plan.Preparations, preparations...)
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
		initializations, err := buildInitializations(registration, request.RunID, servicePlan)
		if err != nil {
			return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
		}
		plan.Initializations = append(plan.Initializations, initializations...)
		if servicePlan.ServerlessOverlay != nil {
			if plan.Projection != nil || servicePlan.ID != "nonprofit-service" {
				return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
			}
			plan.Projection = &environment.ProjectionRequest{ID: marketplaceServerlessProjection}
		}
	}
	return plan, nil
}

func buildInitializations(
	registration EnvironmentRegistration,
	runID string,
	plan marketplaceadapter.ServicePlan,
) ([]environment.PreparationSpec, error) {
	if plan.ID != "nonprofit-service" {
		return nil, nil
	}
	elasticMQFound := false
	for _, infrastructure := range plan.Infrastructure {
		if infrastructure.ID == "elasticmq" && infrastructure.Kind == "container" &&
			infrastructure.Dedicated && infrastructure.Scope == marketplaceadapter.EnvironmentInfrastructureScope {
			if elasticMQFound {
				return nil, ErrMarketplacePlanInvalid
			}
			elasticMQFound = true
		}
	}
	var restPort marketplaceadapter.PortAssignment
	restPortFound := false
	for _, assignment := range plan.Ports {
		if assignment.RequirementID != "elasticmq-rest" {
			continue
		}
		if restPortFound || assignment.Host != containerhost.LoopbackHostIPv4 ||
			assignment.Port < 1 || assignment.Port > 65535 {
			return nil, ErrMarketplacePlanInvalid
		}
		restPort = assignment
		restPortFound = true
	}
	if !elasticMQFound || !restPortFound || !validElasticMQQueueNames(elasticMQQueueNames) {
		return nil, ErrMarketplacePlanInvalid
	}
	planned, err := plannedEnvironment(registration, nil)
	if err != nil {
		return nil, err
	}
	endpoint := "http://" + containerhost.LoopbackHostIPv4 + ":" + strconv.Itoa(restPort.Port)
	runDirectory := filepath.Join(
		registration.RunRoot,
		"environments",
		registration.EnvironmentID,
		"runs",
		runID,
		"initializations",
		plan.ID,
		"elasticmq",
	)
	initializations := []environment.PreparationSpec{{
		ID:          plan.ID + ".initialize.elasticmq.readiness",
		Executable:  marketplaceCurlExecutable,
		Arguments:   elasticMQReadinessArguments(endpoint),
		Environment: append([]string(nil), planned...),
		Directory:   registration.WorktreeRoot,
		RunDirectory: filepath.Join(
			runDirectory,
			"readiness",
		),
		Timeout: elasticMQReadinessTimeout,
	}}
	for index, queueName := range elasticMQQueueNames {
		initializations = append(initializations, environment.PreparationSpec{
			ID:          plan.ID + ".initialize.elasticmq.queue." + strconv.Itoa(index),
			Executable:  marketplaceCurlExecutable,
			Arguments:   elasticMQCreateQueueArguments(endpoint, queueName),
			Environment: append([]string(nil), planned...),
			Directory:   registration.WorktreeRoot,
			RunDirectory: filepath.Join(
				runDirectory,
				"queue-"+strconv.Itoa(index),
			),
			Timeout: elasticMQCreateQueueTimeout,
		})
	}
	return initializations, nil
}

func elasticMQReadinessArguments(endpoint string) []string {
	return []string{
		"--fail", "--silent", "--show-error",
		"--retry", "30",
		"--retry-delay", "1",
		"--retry-max-time", "45",
		"--retry-connrefused",
		"--retry-all-errors",
		"--connect-timeout", "1",
		"--max-time", "3",
		"--request", "POST",
		"--data", "Action=ListQueues",
		"--data", "Version=2012-11-05",
		"--url", endpoint,
	}
}

func elasticMQCreateQueueArguments(endpoint, queueName string) []string {
	return []string{
		"--fail", "--silent", "--show-error",
		"--connect-timeout", "1",
		"--max-time", "5",
		"--request", "POST",
		"--data", "Action=CreateQueue",
		"--data", "QueueName=" + queueName,
		"--data", "Version=2012-11-05",
		"--url", endpoint,
	}
}

func validElasticMQQueueNames(queueNames []string) bool {
	if len(queueNames) != 11 {
		return false
	}
	seen := make(map[string]struct{}, len(queueNames))
	for _, queueName := range queueNames {
		if !elasticMQQueueNamePattern.MatchString(queueName) {
			return false
		}
		if _, duplicate := seen[queueName]; duplicate {
			return false
		}
		seen[queueName] = struct{}{}
	}
	return true
}

func buildPreparations(
	registration EnvironmentRegistration,
	runID string,
	plan marketplaceadapter.ServicePlan,
) ([]environment.PreparationSpec, error) {
	environmentVariables, err := plannedEnvironment(registration, plan.Environment)
	if err != nil {
		return nil, err
	}
	preparations := make([]environment.PreparationSpec, 0, len(plan.PrepareCommands))
	for index, command := range plan.PrepareCommands {
		if command.Executable != marketplaceadapter.RepositoryYarnExecutable ||
			!safeRelativeDirectory(command.WorkingDirectory) {
			return nil, ErrMarketplacePlanInvalid
		}
		arguments := make([]string, 0, len(command.Arguments)+1)
		arguments = append(arguments, registration.YarnCJS)
		for _, argument := range command.Arguments {
			if argument == "" || strings.ContainsRune(argument, 0) {
				return nil, ErrMarketplacePlanInvalid
			}
			arguments = append(arguments, argument)
		}
		commandID := "command-" + strconv.Itoa(index)
		preparations = append(preparations, environment.PreparationSpec{
			ID:          plan.ID + ".prepare." + strconv.Itoa(index),
			Executable:  registration.NodeExecutable,
			Arguments:   arguments,
			Environment: append([]string(nil), environmentVariables...),
			Directory:   filepath.Join(registration.WorktreeRoot, command.WorkingDirectory),
			RunDirectory: filepath.Join(
				registration.RunRoot,
				"environments",
				registration.EnvironmentID,
				"runs",
				runID,
				"preparations",
				plan.ID,
				commandID,
			),
			Timeout: marketplacePreparationTimeout,
		})
	}
	return preparations, nil
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
	environmentVariables, err := plannedEnvironment(registration, plan.Environment)
	if err != nil {
		return environment.ServiceLaunch{}, err
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

func plannedEnvironment(
	registration EnvironmentRegistration,
	variables []marketplaceadapter.EnvironmentVariable,
) ([]string, error) {
	planned := []string{
		"HOME=" + registration.HomeDirectory,
		"PATH=" + registration.ExecutablePath,
		"TMPDIR=" + registration.TemporaryDirectory,
	}
	seen := map[string]struct{}{"HOME": {}, "PATH": {}, "TMPDIR": {}}
	for _, variable := range variables {
		if variable.Name == "" || strings.ContainsAny(variable.Name, "=\x00") ||
			strings.ContainsRune(variable.Value, 0) {
			return nil, ErrMarketplacePlanInvalid
		}
		if _, duplicate := seen[variable.Name]; duplicate {
			return nil, ErrMarketplacePlanInvalid
		}
		seen[variable.Name] = struct{}{}
		planned = append(planned, variable.Name+"="+variable.Value)
	}
	return planned, nil
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

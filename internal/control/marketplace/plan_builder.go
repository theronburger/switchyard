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
	marketplaceServerlessProjection = "marketplace.runtime-projections.v2"
	marketplaceEnvironmentShim      = ".switchyard.env.cjs"
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
	elasticMQQueueNames       = nonprofitQueueNames()
)

func nonprofitQueueNames() []string {
	definition, found := marketplaceadapter.DefaultCatalog().Definition("nonprofit-service")
	if !found {
		return nil
	}
	names := make([]string, 0, len(definition.Queues))
	for _, queue := range definition.Queues {
		names = append(names, queue.Name)
	}
	return names
}

func validElasticMQQueueNames(queueNames []string) bool {
	if len(queueNames) != len(elasticMQQueueNames) {
		return false
	}
	queues := make([]marketplaceadapter.QueueDefinition, 0, len(queueNames))
	for _, name := range queueNames {
		queues = append(queues, marketplaceadapter.QueueDefinition{
			Name: name, FIFO: strings.HasSuffix(name, ".fifo"),
			ContentBasedDeduplication: strings.HasSuffix(name, ".fifo"),
		})
	}
	return validElasticMQQueues(queues)
}

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
	targetID := request.Intent.TargetID
	if targetID == "" {
		targetID = marketplaceadapter.DefaultRuntimeTargetID()
	}
	targetEnvironment, knownTarget := marketplaceadapter.RuntimeTargetEnvironment(targetID)
	if !knownTarget {
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

	plan := environment.ExecutionPlan{
		Projection: &environment.ProjectionRequest{ID: marketplaceServerlessProjection},
	}
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
		servicePlan.Environment, err = mergePlannedEnvironment(servicePlan.Environment, targetEnvironment)
		if err != nil {
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
		initializations, err := buildInitializations(registration, request.RunID, servicePlan)
		if err != nil {
			return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
		}
		plan.Initializations = append(plan.Initializations, initializations...)
	}
	plan.Infrastructure, err = buildInfrastructureGoals(registration, request.RunID, servicePlans)
	if err != nil {
		return environment.ExecutionPlan{}, ErrMarketplacePlanInvalid
	}
	return plan, nil
}

func mergePlannedEnvironment(
	base []marketplaceadapter.EnvironmentVariable,
	additional []marketplaceadapter.EnvironmentVariable,
) ([]marketplaceadapter.EnvironmentVariable, error) {
	values := make(map[string]string, len(base)+len(additional))
	for _, variable := range append(append([]marketplaceadapter.EnvironmentVariable{}, base...), additional...) {
		if variable.Name == "" || strings.ContainsRune(variable.Name, 0) || strings.ContainsRune(variable.Value, 0) {
			return nil, ErrMarketplacePlanInvalid
		}
		if current, exists := values[variable.Name]; exists && current != variable.Value {
			return nil, ErrMarketplacePlanInvalid
		}
		values[variable.Name] = variable.Value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	merged := make([]marketplaceadapter.EnvironmentVariable, 0, len(names))
	for _, name := range names {
		merged = append(merged, marketplaceadapter.EnvironmentVariable{Name: name, Value: values[name]})
	}
	return merged, nil
}

func buildInitializations(
	registration EnvironmentRegistration,
	runID string,
	plan marketplaceadapter.ServicePlan,
) ([]environment.PreparationSpec, error) {
	if len(plan.Queues) == 0 && !hasInfrastructure(plan, "elasticmq") {
		return nil, nil
	}
	elasticMQFound := hasInfrastructure(plan, "elasticmq")
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
	if !elasticMQFound || !restPortFound || !validElasticMQQueues(plan.Queues) {
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
		ID:        plan.ID + ".initialize.elasticmq.readiness",
		ServiceID: plan.ID,
		LogReference: filepath.ToSlash(filepath.Join(
			runID, "initializations", plan.ID, "elasticmq", "readiness",
		)),
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
	for index, queue := range plan.Queues {
		initializations = append(initializations, environment.PreparationSpec{
			ID:        plan.ID + ".initialize.elasticmq.queue." + strconv.Itoa(index),
			ServiceID: plan.ID,
			LogReference: filepath.ToSlash(filepath.Join(
				runID, "initializations", plan.ID, "elasticmq", "queue-"+strconv.Itoa(index),
			)),
			Executable:  marketplaceCurlExecutable,
			Arguments:   elasticMQCreateQueueDefinitionArguments(endpoint, queue),
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
	return elasticMQCreateQueueDefinitionArguments(endpoint, marketplaceadapter.QueueDefinition{Name: queueName})
}

func elasticMQCreateQueueDefinitionArguments(endpoint string, queue marketplaceadapter.QueueDefinition) []string {
	arguments := []string{
		"--fail", "--silent", "--show-error",
		"--connect-timeout", "1",
		"--max-time", "5",
		"--request", "POST",
		"--data", "Action=CreateQueue",
		"--data", "QueueName=" + queue.Name,
		"--data", "Version=2012-11-05",
	}
	if queue.FIFO {
		arguments = append(arguments,
			"--data", "Attribute.1.Name=FifoQueue",
			"--data", "Attribute.1.Value=true",
			"--data", "Attribute.2.Name=ContentBasedDeduplication",
			"--data", "Attribute.2.Value="+strconv.FormatBool(queue.ContentBasedDeduplication),
		)
	}
	return append(arguments, "--url", endpoint)
}

func validElasticMQQueues(queues []marketplaceadapter.QueueDefinition) bool {
	seen := make(map[string]struct{}, len(queues))
	for _, queue := range queues {
		baseName := strings.TrimSuffix(queue.Name, ".fifo")
		if !elasticMQQueueNamePattern.MatchString(baseName) || queue.FIFO != strings.HasSuffix(queue.Name, ".fifo") ||
			(!queue.FIFO && queue.ContentBasedDeduplication) {
			return false
		}
		if _, duplicate := seen[queue.Name]; duplicate {
			return false
		}
		seen[queue.Name] = struct{}{}
	}
	return true
}

func hasInfrastructure(plan marketplaceadapter.ServicePlan, infrastructureID string) bool {
	found := false
	for _, infrastructure := range plan.Infrastructure {
		if infrastructure.ID != infrastructureID {
			continue
		}
		if found || infrastructure.Kind != "container" || !infrastructure.Dedicated ||
			infrastructure.Scope != marketplaceadapter.EnvironmentInfrastructureScope {
			return false
		}
		found = true
	}
	return found
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
			ID:           plan.ID + ".prepare." + strconv.Itoa(index),
			ServiceID:    plan.ID,
			LogReference: filepath.ToSlash(filepath.Join(runID, "preparations", plan.ID, commandID)),
			Executable:   registration.NodeExecutable,
			Arguments:    arguments,
			Environment:  append([]string(nil), environmentVariables...),
			Directory:    filepath.Join(registration.WorktreeRoot, command.WorkingDirectory),
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
	preloads := []string{filepath.Join(registration.WorktreeRoot, marketplaceEnvironmentShim)}
	if endpointShimDirectory := endpointShimDirectory(plan.ID); endpointShimDirectory != "" {
		shimPath := filepath.Join(registration.WorktreeRoot, endpointShimDirectory, ".switchyard.endpoints.cjs")
		preloads = append(preloads, shimPath)
	}
	environmentVariables, err = appendNodePreloads(environmentVariables, preloads...)
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

func endpointShimDirectory(serviceID string) string {
	switch serviceID {
	case "donation-batch-service":
		return filepath.Join("services", "donation-batch-service")
	case "slack-service":
		return filepath.Join("services", "slack-service")
	default:
		return ""
	}
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

func appendNodePreloads(environmentVariables []string, paths ...string) ([]string, error) {
	if len(paths) == 0 {
		return append([]string(nil), environmentVariables...), nil
	}
	result := append([]string(nil), environmentVariables...)
	optionsIndex := -1
	options := ""
	for index, variable := range result {
		name, value, found := strings.Cut(variable, "=")
		if !found || name == "" {
			return nil, ErrMarketplacePlanInvalid
		}
		if name != "NODE_OPTIONS" {
			continue
		}
		if optionsIndex != -1 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, ErrMarketplacePlanInvalid
		}
		optionsIndex = index
		options = strings.TrimSpace(value)
	}
	for _, preloadPath := range paths {
		if !filepath.IsAbs(preloadPath) || filepath.Clean(preloadPath) != preloadPath ||
			strings.ContainsAny(preloadPath, "\x00\r\n") {
			return nil, ErrMarketplacePlanInvalid
		}
		option := "--require=" + preloadPath
		if strings.ContainsAny(preloadPath, " \t\"") {
			option = "--require=" + strconv.Quote(preloadPath)
		}
		if options != "" {
			options += " "
		}
		options += option
	}
	entry := "NODE_OPTIONS=" + options
	if optionsIndex == -1 {
		result = append(result, entry)
	} else {
		result[optionsIndex] = entry
	}
	return result, nil
}

func buildInfrastructureGoals(
	registration EnvironmentRegistration,
	runID string,
	servicePlans []marketplaceadapter.ServicePlan,
) ([]containerhost.Goal, error) {
	type groupedInfrastructure struct {
		requirement marketplaceadapter.InfrastructureRequirement
		bindings    []containerhost.PortBinding
	}
	grouped := make(map[string]groupedInfrastructure)
	for _, servicePlan := range servicePlans {
		assignments := make(map[string]marketplaceadapter.PortAssignment, len(servicePlan.Ports))
		for _, assignment := range servicePlan.Ports {
			if _, duplicate := assignments[assignment.RequirementID]; duplicate {
				return nil, ErrMarketplacePlanInvalid
			}
			assignments[assignment.RequirementID] = assignment
		}
		for _, infrastructure := range servicePlan.Infrastructure {
			if infrastructure.Kind != "container" || !infrastructure.Dedicated ||
				infrastructure.Scope != marketplaceadapter.EnvironmentInfrastructureScope {
				return nil, ErrMarketplacePlanInvalid
			}
			current, found := grouped[infrastructure.ID]
			if found && (current.requirement.Image != infrastructure.Image || current.requirement.Kind != infrastructure.Kind) {
				return nil, ErrMarketplacePlanInvalid
			}
			if !found {
				current.requirement = infrastructure
			}
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
				current.bindings = append(current.bindings, containerhost.PortBinding{
					Host:          containerhost.LoopbackHostIPv4,
					HostPort:      assignment.Port,
					ContainerPort: infrastructurePort.ContainerPort,
					Protocol:      containerhost.PortProtocolTCP,
				})
			}
			grouped[infrastructure.ID] = current
		}
	}
	ids := make([]string, 0, len(grouped))
	for id := range grouped {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	goals := make([]containerhost.Goal, 0, len(ids))
	for _, infrastructureID := range ids {
		infrastructure := grouped[infrastructureID]
		identity := containerhost.Identity{
			EnvironmentID: registration.EnvironmentID,
			ServiceID:     "shared." + infrastructureID,
			RunID:         runID,
			InstanceID:    registration.DaemonInstanceID,
		}
		if identity.Validate() != nil {
			return nil, ErrMarketplacePlanInvalid
		}
		goals = append(goals, containerhost.Goal{
			Kind:         containerhost.ResourceContainer,
			Name:         infrastructureName(registration.EnvironmentID, runID, infrastructureID),
			Image:        infrastructure.requirement.Image,
			PortBindings: infrastructure.bindings,
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

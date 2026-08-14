package marketplace

import (
	"fmt"
	"sort"
	"strconv"
)

func (catalog Catalog) Definition(serviceID string) (ServiceDefinition, bool) {
	definition, found := catalog.definitions[serviceID]
	if !found {
		return ServiceDefinition{}, false
	}
	return cloneServiceDefinition(definition), true
}

func (catalog Catalog) PlanEnvironment(
	serviceIDs []string,
	assignedPorts map[string]map[string]int,
) ([]ServicePlan, error) {
	definitions := make([]ServiceDefinition, 0, len(serviceIDs))
	seenServices := make(map[string]struct{}, len(serviceIDs))
	for _, serviceID := range serviceIDs {
		if _, exists := seenServices[serviceID]; exists {
			return nil, fmt.Errorf("service %q was requested more than once", serviceID)
		}
		seenServices[serviceID] = struct{}{}

		definition, found := catalog.definitions[serviceID]
		if !found {
			return nil, fmt.Errorf("service %q is not in the Marketplace catalog", serviceID)
		}
		if err := validateServiceDefinition(definition); err != nil {
			return nil, fmt.Errorf("service %q definition: %w", serviceID, err)
		}
		definitions = append(definitions, definition)
	}
	if err := validateEnvironmentPortAssignments(definitions, assignedPorts); err != nil {
		return nil, err
	}

	routingEnvironment, err := planRoutingEnvironment(definitions, assignedPorts)
	if err != nil {
		return nil, err
	}

	plans := make([]ServicePlan, 0, len(definitions))
	for _, definition := range definitions {
		plan, err := planService(definition, assignedPorts[definition.ID], routingEnvironment)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func planRoutingEnvironment(
	definitions []ServiceDefinition,
	assignedPorts map[string]map[string]int,
) (map[string]string, error) {
	routingEnvironment := make(map[string]string)
	for _, definition := range definitions {
		ports, err := validatePortAssignments(definition, assignedPorts[definition.ID])
		if err != nil {
			return nil, err
		}
		for _, route := range definition.PublishedRoutes {
			value, err := renderEnvironmentValue(route, ports)
			if err != nil {
				return nil, fmt.Errorf("service %q route %q: %w", definition.ID, route.Name, err)
			}
			if err := addEnvironmentValue(routingEnvironment, route.Name, value); err != nil {
				return nil, err
			}
		}
	}
	return routingEnvironment, nil
}

func planService(
	definition ServiceDefinition,
	assignedPorts map[string]int,
	routingEnvironment map[string]string,
) (ServicePlan, error) {
	ports, err := validatePortAssignments(definition, assignedPorts)
	if err != nil {
		return ServicePlan{}, err
	}

	environment := make(map[string]string, len(routingEnvironment)+len(definition.EnvironmentBindings))
	for name, value := range routingEnvironment {
		environment[name] = value
	}
	for _, binding := range definition.EnvironmentBindings {
		value, err := renderEnvironmentValue(binding, ports)
		if err != nil {
			return ServicePlan{}, fmt.Errorf("service %q environment %q: %w", definition.ID, binding.Name, err)
		}
		if err := addEnvironmentValue(environment, binding.Name, value); err != nil {
			return ServicePlan{}, err
		}
	}

	return ServicePlan{
		ID:                definition.ID,
		DisplayName:       definition.DisplayName,
		Kind:              definition.Kind,
		WorkspacePackage:  definition.WorkspacePackage,
		Ports:             orderedPortAssignments(definition, ports),
		PrepareCommands:   cloneCommands(definition.PrepareCommands),
		RunCommand:        cloneCommand(definition.RunCommand),
		Environment:       orderedEnvironment(environment),
		Readiness:         cloneProbes(definition.Readiness),
		Health:            cloneProbes(definition.Health),
		Infrastructure:    cloneInfrastructure(definition.Infrastructure),
		ServerlessOverlay: cloneServerlessOverlay(definition.ServerlessOverlay),
	}, nil
}

func renderEnvironmentValue(binding EnvironmentBinding, ports map[string]int) (string, error) {
	port, exists := ports[binding.PortRequirement]
	if !exists {
		return "", fmt.Errorf("port %q is not assigned", binding.PortRequirement)
	}
	return formatEnvironmentValue(binding.Format, port)
}

func formatEnvironmentValue(format EnvironmentValueFormat, port int) (string, error) {
	switch format {
	case EnvironmentValueDecimalPort:
		return strconv.Itoa(port), nil
	case EnvironmentValueHTTPURL:
		return "http://127.0.0.1:" + strconv.Itoa(port), nil
	default:
		return "", fmt.Errorf("environment value format %q is unknown", format)
	}
}

func addEnvironmentValue(environment map[string]string, name string, value string) error {
	if currentValue, exists := environment[name]; exists && currentValue != value {
		return fmt.Errorf("environment variable %q has conflicting values", name)
	}
	environment[name] = value
	return nil
}

func orderedPortAssignments(definition ServiceDefinition, ports map[string]int) []PortAssignment {
	assignments := make([]PortAssignment, 0, len(definition.PortRequirements))
	for _, requirement := range definition.PortRequirements {
		assignments = append(assignments, PortAssignment{
			RequirementID: requirement.ID,
			Host:          requirement.BindHost,
			Port:          ports[requirement.ID],
		})
	}
	return assignments
}

func orderedEnvironment(environment map[string]string) []EnvironmentVariable {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)

	variables := make([]EnvironmentVariable, 0, len(names))
	for _, name := range names {
		variables = append(variables, EnvironmentVariable{Name: name, Value: environment[name]})
	}
	return variables
}

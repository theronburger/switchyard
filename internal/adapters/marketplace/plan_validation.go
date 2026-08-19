package marketplace

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var queueNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}(?:\.fifo)?$`)
var serverlessPluginPattern = regexp.MustCompile(`^(?:@[A-Za-z0-9_.-]+/)?[A-Za-z0-9_.-]+$`)

func validateServiceDefinition(definition ServiceDefinition) error {
	if definition.ID == "" || definition.WorkspacePackage == "" {
		return fmt.Errorf("service ID and workspace package are required")
	}
	if definition.RunCommand.Executable == "" || len(definition.RunCommand.Arguments) == 0 {
		return fmt.Errorf("run command is required")
	}

	portRequirements, err := validatePortRequirements(definition.PortRequirements)
	if err != nil {
		return err
	}
	if definition.HasHTTPListener {
		if _, exists := portRequirements["http"]; !exists {
			return fmt.Errorf("HTTP-listening service requires an HTTP port")
		}
	}
	if err := validateEnvironmentBindings(definition, portRequirements); err != nil {
		return err
	}
	if err := validateProbes(definition, portRequirements); err != nil {
		return err
	}
	if err := validateInfrastructure(definition.Infrastructure, portRequirements); err != nil {
		return err
	}
	if err := validateQueues(definition.Queues, definition.Infrastructure); err != nil {
		return err
	}
	if definition.ServerlessOverlay != nil {
		return validateServerlessOverlay(*definition.ServerlessOverlay, portRequirements)
	}
	return nil
}

func validateQueues(queues []QueueDefinition, infrastructure []InfrastructureRequirement) error {
	if len(queues) == 0 {
		return nil
	}
	hasElasticMQ := false
	for _, requirement := range infrastructure {
		if requirement.ID == "elasticmq" {
			hasElasticMQ = true
		}
	}
	if !hasElasticMQ {
		return fmt.Errorf("queue definitions require ElasticMQ infrastructure")
	}
	seen := make(map[string]struct{}, len(queues))
	for _, queue := range queues {
		if !queueNamePattern.MatchString(queue.Name) || queue.FIFO != strings.HasSuffix(queue.Name, ".fifo") ||
			(!queue.FIFO && queue.ContentBasedDeduplication) {
			return fmt.Errorf("queue definition is invalid")
		}
		if _, duplicate := seen[queue.Name]; duplicate {
			return fmt.Errorf("queue %q is duplicated", queue.Name)
		}
		seen[queue.Name] = struct{}{}
	}
	return nil
}

func validatePortRequirements(requirements []PortRequirement) (map[string]struct{}, error) {
	known := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		if requirement.ID == "" || requirement.Purpose == "" || requirement.BindHost == "" {
			return nil, fmt.Errorf("port requirement ID, purpose, and bind host are required")
		}
		if _, exists := known[requirement.ID]; exists {
			return nil, fmt.Errorf("duplicate port requirement %q", requirement.ID)
		}
		known[requirement.ID] = struct{}{}

		preferenceSources := 0
		if requirement.PreferredPort != 0 {
			preferenceSources++
		}
		if requirement.PreferredPortEnvironment != "" {
			preferenceSources++
		}
		if requirement.PreferredRelativeTo != "" {
			preferenceSources++
		}
		if preferenceSources != 1 {
			return nil, fmt.Errorf("port requirement %q must have exactly one preferred source", requirement.ID)
		}
		if requirement.PreferredPort != 0 && (requirement.PreferredPort < 1 || requirement.PreferredPort > 65535) {
			return nil, fmt.Errorf("port requirement %q has an invalid preferred port", requirement.ID)
		}
		if requirement.PreferredRelativeTo == "" && requirement.PreferredOffset != 0 {
			return nil, fmt.Errorf("port requirement %q has an offset without a relative port", requirement.ID)
		}
		if requirement.PreferredRelativeTo != "" && requirement.PreferredOffset == 0 {
			return nil, fmt.Errorf("port requirement %q has a relative port without an offset", requirement.ID)
		}
	}

	for _, requirement := range requirements {
		if requirement.PreferredRelativeTo == "" {
			continue
		}
		if _, exists := known[requirement.PreferredRelativeTo]; !exists {
			return nil, fmt.Errorf(
				"port requirement %q refers to unknown preferred port %q",
				requirement.ID,
				requirement.PreferredRelativeTo,
			)
		}
	}
	return known, nil
}

func validateEnvironmentBindings(
	definition ServiceDefinition,
	portRequirements map[string]struct{},
) error {
	bindings := append(append([]EnvironmentBinding{}, definition.EnvironmentBindings...), definition.PublishedRoutes...)
	for _, binding := range bindings {
		if binding.Name == "" {
			return fmt.Errorf("environment binding name is required")
		}
		if _, exists := portRequirements[binding.PortRequirement]; !exists {
			return fmt.Errorf("environment binding %q refers to unknown port %q", binding.Name, binding.PortRequirement)
		}
		if binding.Format != EnvironmentValueDecimalPort && binding.Format != EnvironmentValueHTTPURL &&
			binding.Format != EnvironmentValueBrowserHTTPURL {
			return fmt.Errorf("environment binding %q has unknown format %q", binding.Name, binding.Format)
		}
	}
	return nil
}

func validateProbes(definition ServiceDefinition, portRequirements map[string]struct{}) error {
	probes := append(append([]Probe{}, definition.Readiness...), definition.Health...)
	for _, probe := range probes {
		if err := validateProbe(probe, portRequirements); err != nil {
			return err
		}
	}
	return nil
}

func validateProbe(probe Probe, portRequirements map[string]struct{}) error {
	if _, exists := portRequirements[probe.PortRequirement]; !exists {
		return fmt.Errorf("probe refers to unknown port %q", probe.PortRequirement)
	}
	switch probe.Kind {
	case ProbeKindTCP:
		if probe.Method != "" || probe.Path != "" || len(probe.AcceptedStatuses) != 0 {
			return fmt.Errorf("TCP probe for %q contains HTTP fields", probe.PortRequirement)
		}
	case ProbeKindHTTP:
		if probe.Method == "" || probe.Path == "" || len(probe.AcceptedStatuses) == 0 {
			return fmt.Errorf("HTTP probe for %q is incomplete", probe.PortRequirement)
		}
		for _, accepted := range probe.AcceptedStatuses {
			if accepted.Minimum < 100 || accepted.Maximum > 599 || accepted.Minimum > accepted.Maximum {
				return fmt.Errorf("HTTP probe for %q has an invalid status range", probe.PortRequirement)
			}
		}
	default:
		return fmt.Errorf("probe kind %q is unknown", probe.Kind)
	}
	return nil
}

func validateInfrastructure(
	requirements []InfrastructureRequirement,
	portRequirements map[string]struct{},
) error {
	for _, infrastructure := range requirements {
		if infrastructure.ID == "" || infrastructure.Kind == "" || infrastructure.Scope == "" {
			return fmt.Errorf("infrastructure ID, kind, and scope are required")
		}
		for _, containerPort := range infrastructure.Ports {
			if _, exists := portRequirements[containerPort.PortRequirement]; !exists {
				return fmt.Errorf(
					"infrastructure %q refers to unknown port %q",
					infrastructure.ID,
					containerPort.PortRequirement,
				)
			}
			if containerPort.ContainerPort < 1 || containerPort.ContainerPort > 65535 {
				return fmt.Errorf("infrastructure %q has an invalid container port", infrastructure.ID)
			}
		}
		for _, probe := range infrastructure.Readiness {
			if err := validateProbe(probe, portRequirements); err != nil {
				return fmt.Errorf("infrastructure %q: %w", infrastructure.ID, err)
			}
		}
	}
	return nil
}

func validateServerlessOverlay(
	overlay ServerlessOverlay,
	portRequirements map[string]struct{},
) error {
	if overlay.Directory == "" || overlay.Filename == "" || overlay.SourceConfig == "" {
		return fmt.Errorf("Serverless overlay directory, filename, and source config are required") //nolint:staticcheck // Product-facing diagnostic starts with the service name.
	}
	if len(overlay.Overrides) == 0 {
		return fmt.Errorf("Serverless overlay requires at least one override") //nolint:staticcheck // Product-facing diagnostic starts with the service name.
	}
	seenPlugins := make(map[string]struct{}, len(overlay.Plugins))
	for _, plugin := range overlay.Plugins {
		if !serverlessPluginPattern.MatchString(plugin) {
			return fmt.Errorf("Serverless overlay plugin is invalid") //nolint:staticcheck // Product-facing diagnostic starts with the service name.
		}
		if _, duplicate := seenPlugins[plugin]; duplicate {
			return fmt.Errorf("Serverless overlay plugin is duplicated") //nolint:staticcheck // Product-facing diagnostic starts with the service name.
		}
		seenPlugins[plugin] = struct{}{}
	}
	for _, override := range overlay.Overrides {
		if len(override.ConfigurationPath) == 0 {
			return fmt.Errorf("Serverless override configuration path is required")
		}
		if _, exists := portRequirements[override.PortRequirement]; !exists {
			return fmt.Errorf("Serverless override refers to unknown port %q", override.PortRequirement)
		}
		if override.Format != OverlayValueIntegerPort && override.Format != OverlayValueHTTPURL &&
			override.Format != OverlayValueLoopback {
			return fmt.Errorf("Serverless override has unknown format %q", override.Format)
		}
		if override.URLPath != "" && (override.Format != OverlayValueHTTPURL ||
			!strings.HasPrefix(override.URLPath, "/") || path.Clean(override.URLPath) != override.URLPath ||
			strings.ContainsAny(override.URLPath, "?#\x00")) {
			return fmt.Errorf("Serverless override URL path is invalid")
		}
	}
	return nil
}

func validateEnvironmentPortAssignments(
	definitions []ServiceDefinition,
	assignedPorts map[string]map[string]int,
) error {
	type portOwner struct {
		serviceID     string
		requirementID string
	}
	owners := make(map[string]portOwner)
	for _, definition := range definitions {
		ports, err := validatePortAssignments(definition, assignedPorts[definition.ID])
		if err != nil {
			return err
		}
		for _, requirement := range definition.PortRequirements {
			address := requirement.BindHost + ":" + strconv.Itoa(ports[requirement.ID])
			if owner, exists := owners[address]; exists {
				return fmt.Errorf(
					"service %q port %q conflicts with service %q port %q at %s",
					definition.ID,
					requirement.ID,
					owner.serviceID,
					owner.requirementID,
					address,
				)
			}
			owners[address] = portOwner{serviceID: definition.ID, requirementID: requirement.ID}
		}
	}
	return nil
}

func validatePortAssignments(
	definition ServiceDefinition,
	assigned map[string]int,
) (map[string]int, error) {
	if assigned == nil {
		return nil, fmt.Errorf("service %q has no port assignments", definition.ID)
	}

	requirements := make(map[string]struct{}, len(definition.PortRequirements))
	validated := make(map[string]int, len(definition.PortRequirements))
	for _, requirement := range definition.PortRequirements {
		requirements[requirement.ID] = struct{}{}
		port, exists := assigned[requirement.ID]
		if !exists {
			return nil, fmt.Errorf("service %q has no assignment for port %q", definition.ID, requirement.ID)
		}
		if port < 1 || port > 65535 {
			return nil, fmt.Errorf("service %q port %q is outside 1-65535", definition.ID, requirement.ID)
		}
		validated[requirement.ID] = port
	}
	for requirementID := range assigned {
		if _, exists := requirements[requirementID]; !exists {
			return nil, fmt.Errorf("service %q has an assignment for unknown port %q", definition.ID, requirementID)
		}
	}
	return validated, nil
}

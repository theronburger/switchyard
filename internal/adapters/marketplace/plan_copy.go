package marketplace

func cloneServiceDefinition(definition ServiceDefinition) ServiceDefinition {
	cloned := definition
	cloned.PortRequirements = append([]PortRequirement(nil), definition.PortRequirements...)
	cloned.PrepareCommands = cloneCommands(definition.PrepareCommands)
	cloned.RunCommand = cloneCommand(definition.RunCommand)
	cloned.EnvironmentBindings = append([]EnvironmentBinding(nil), definition.EnvironmentBindings...)
	cloned.PublishedRoutes = append([]EnvironmentBinding(nil), definition.PublishedRoutes...)
	cloned.Readiness = cloneProbes(definition.Readiness)
	cloned.Health = cloneProbes(definition.Health)
	cloned.Infrastructure = cloneInfrastructure(definition.Infrastructure)
	cloned.ServerlessOverlay = cloneServerlessOverlay(definition.ServerlessOverlay)
	return cloned
}

func cloneCommands(commands []PlannedCommand) []PlannedCommand {
	cloned := make([]PlannedCommand, len(commands))
	for index, command := range commands {
		cloned[index] = cloneCommand(command)
	}
	return cloned
}

func cloneCommand(command PlannedCommand) PlannedCommand {
	command.Arguments = append([]string(nil), command.Arguments...)
	return command
}

func cloneProbes(probes []Probe) []Probe {
	cloned := append([]Probe(nil), probes...)
	for index := range cloned {
		cloned[index].AcceptedStatuses = append([]HTTPStatusRange(nil), probes[index].AcceptedStatuses...)
	}
	return cloned
}

func cloneInfrastructure(requirements []InfrastructureRequirement) []InfrastructureRequirement {
	cloned := append([]InfrastructureRequirement(nil), requirements...)
	for index := range cloned {
		cloned[index].Ports = append([]ContainerPort(nil), requirements[index].Ports...)
		cloned[index].Readiness = cloneProbes(requirements[index].Readiness)
	}
	return cloned
}

func cloneServerlessOverlay(overlay *ServerlessOverlay) *ServerlessOverlay {
	if overlay == nil {
		return nil
	}
	cloned := *overlay
	cloned.Overrides = append([]ServerlessOverride(nil), overlay.Overrides...)
	for index := range cloned.Overrides {
		cloned.Overrides[index].ConfigurationPath = append(
			[]string(nil),
			overlay.Overrides[index].ConfigurationPath...,
		)
	}
	return &cloned
}

package marketplace

func DefaultCatalog() Catalog {
	organizer := organizerDefinition()
	nonprofitService := nonprofitServiceDefinition()
	return Catalog{definitions: map[string]ServiceDefinition{
		organizer.ID:        organizer,
		nonprofitService.ID: nonprofitService,
	}}
}

func ServiceIDForPackage(packageName string) string {
	if packageName == "wallet-service" {
		return "wallet"
	}
	return packageName
}

func PackageNameForServiceID(serviceID string) string {
	if serviceID == "wallet" {
		return "wallet-service"
	}
	return serviceID
}

func organizerDefinition() ServiceDefinition {
	return ServiceDefinition{
		ID:               "organizer",
		DisplayName:      "Organizer",
		Kind:             ServiceKindWeb,
		WorkspacePackage: "organizer",
		PortRequirements: []PortRequirement{
			{
				ID:                       "http",
				Purpose:                  "http",
				BindHost:                 "127.0.0.1",
				PreferredPortEnvironment: "DEED_ORGANIZER_PORT",
			},
		},
		PrepareCommands: []PlannedCommand{
			{
				Executable: RepositoryYarnExecutable,
				Arguments: []string{
					"turbo",
					"run",
					"build:no-dependencies",
					"--filter=organizer",
					"--ui=stream",
				},
				WorkingDirectory: ".",
			},
		},
		RunCommand: PlannedCommand{
			Executable:       RepositoryYarnExecutable,
			Arguments:        []string{"workspace", "organizer", "start"},
			WorkingDirectory: ".",
		},
		EnvironmentBindings: []EnvironmentBinding{
			{Name: "DEED_ORGANIZER_PORT", PortRequirement: "http", Format: EnvironmentValueDecimalPort},
			{Name: "PORT", PortRequirement: "http", Format: EnvironmentValueDecimalPort},
		},
		Readiness: []Probe{
			{Kind: ProbeKindTCP, PortRequirement: "http"},
		},
		Health: []Probe{
			{
				Kind:            ProbeKindHTTP,
				PortRequirement: "http",
				Method:          "GET",
				Path:            "/",
				AcceptedStatuses: []HTTPStatusRange{
					{Minimum: 200, Maximum: 399},
				},
			},
		},
	}
}

func nonprofitServiceDefinition() ServiceDefinition {
	return ServiceDefinition{
		ID:               "nonprofit-service",
		DisplayName:      "Nonprofit Service",
		Kind:             ServiceKindAPI,
		WorkspacePackage: "nonprofit-service",
		PortRequirements: []PortRequirement{
			{
				ID:                       "http",
				Purpose:                  "http",
				BindHost:                 "127.0.0.1",
				PreferredPortEnvironment: "DEED_NONPROFIT_SERVICE_PORT",
			},
			{
				ID:                  "lambda",
				Purpose:             "lambda",
				BindHost:            "127.0.0.1",
				PreferredRelativeTo: "http",
				PreferredOffset:     1000,
			},
			{
				ID:            "elasticmq-rest",
				Purpose:       "elasticmq-rest",
				BindHost:      "127.0.0.1",
				PreferredPort: 9324,
			},
			{
				ID:                  "elasticmq-ui",
				Purpose:             "elasticmq-ui",
				BindHost:            "127.0.0.1",
				PreferredRelativeTo: "elasticmq-rest",
				PreferredOffset:     1,
			},
		},
		PrepareCommands: []PlannedCommand{
			{
				Executable: RepositoryYarnExecutable,
				Arguments: []string{
					"turbo",
					"run",
					"build:no-dependencies",
					"--filter=nonprofit-service",
					"--ui=stream",
				},
				WorkingDirectory: ".",
			},
		},
		RunCommand: PlannedCommand{
			Executable: RepositoryYarnExecutable,
			Arguments: []string{
				"workspace",
				"nonprofit-service",
				"sls:withEnv",
				"offline",
				"start",
				"--stage",
				"local",
				"--config",
				".switchyard.serverless.ts",
			},
			WorkingDirectory: ".",
		},
		EnvironmentBindings: []EnvironmentBinding{
			{
				Name:            "DEED_NONPROFIT_SERVICE_PORT",
				PortRequirement: "http",
				Format:          EnvironmentValueDecimalPort,
			},
		},
		PublishedRoutes: []EnvironmentBinding{
			{
				Name:            "DEED_NONPROFIT_API_URI",
				PortRequirement: "http",
				Format:          EnvironmentValueHTTPURL,
			},
		},
		Readiness: []Probe{
			{Kind: ProbeKindTCP, PortRequirement: "http"},
			{Kind: ProbeKindTCP, PortRequirement: "lambda"},
		},
		Health: []Probe{
			{
				Kind:            ProbeKindHTTP,
				PortRequirement: "http",
				Method:          "GET",
				Path:            "/",
				AcceptedStatuses: []HTTPStatusRange{
					{Minimum: 200, Maximum: 499},
				},
			},
		},
		Infrastructure: []InfrastructureRequirement{
			{
				ID:          "elasticmq",
				DisplayName: "ElasticMQ",
				Kind:        "container",
				Scope:       EnvironmentInfrastructureScope,
				Image:       "softwaremill/elasticmq",
				Dedicated:   true,
				Ports: []ContainerPort{
					{PortRequirement: "elasticmq-rest", ContainerPort: 9324},
					{PortRequirement: "elasticmq-ui", ContainerPort: 9325},
				},
				Readiness: []Probe{
					{Kind: ProbeKindTCP, PortRequirement: "elasticmq-rest"},
				},
			},
		},
		ServerlessOverlay: &ServerlessOverlay{
			Directory:    "services/nonprofit-service",
			Filename:     ".switchyard.serverless.ts",
			SourceConfig: "serverless.ts",
			Overrides: []ServerlessOverride{
				{
					ConfigurationPath: []string{"custom", "serverless-offline", "httpPort"},
					PortRequirement:   "http",
					Format:            OverlayValueIntegerPort,
				},
				{
					ConfigurationPath: []string{"custom", "serverless-offline", "lambdaPort"},
					PortRequirement:   "lambda",
					Format:            OverlayValueIntegerPort,
				},
				{
					ConfigurationPath: []string{"custom", "serverless-offline-sqs", "endpoint"},
					PortRequirement:   "elasticmq-rest",
					Format:            OverlayValueHTTPURL,
				},
			},
		},
	}
}

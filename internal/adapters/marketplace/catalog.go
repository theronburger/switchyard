package marketplace

import "sort"

func DefaultCatalog() Catalog {
	api := apiDefinition()
	app := appDefinition()
	organizer := organizerDefinition()
	definitions := map[string]ServiceDefinition{
		api.ID:       api,
		app.ID:       app,
		organizer.ID: organizer,
	}
	for _, definition := range serverlessServiceDefinitions() {
		definitions[definition.ID] = definition
	}
	return Catalog{definitions: definitions}
}

func (catalog Catalog) ServiceIDs() []string {
	serviceIDs := make([]string, 0, len(catalog.definitions))
	for serviceID := range catalog.definitions {
		serviceIDs = append(serviceIDs, serviceID)
	}
	sort.Strings(serviceIDs)
	return serviceIDs
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

func apiDefinition() ServiceDefinition {
	return simpleWebDefinition(
		"api",
		"API",
		ServiceKindAPI,
		"api",
		"DEED_API_PORT",
		[]EnvironmentBinding{
			{Name: "DEED_LEGACY_API_URI", PortRequirement: "http", Format: EnvironmentValueHTTPURL},
			{Name: "DEED_API_URI", PortRequirement: "http", Format: EnvironmentValueHTTPURL},
			{Name: "DEED_DONATION_SERVICE_LEGACY_API_URI", PortRequirement: "http", Format: EnvironmentValueHTTPURL},
			{Name: "DEED_SLACK_SERVICE_LEGACY_API_URI", PortRequirement: "http", Format: EnvironmentValueHTTPURL},
			{Name: "DEED_WALLET_SERVICE_LEGACY_API_URI", PortRequirement: "http", Format: EnvironmentValueHTTPURL},
		},
	)
}

func appDefinition() ServiceDefinition {
	definition := simpleWebDefinition(
		"app",
		"App",
		ServiceKindWeb,
		"app",
		"DEED_APP_PORT",
		[]EnvironmentBinding{
			{Name: "DEED_WEB_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
			{Name: "DEED_CLIENT_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
			{Name: "DEED_APP_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
			{Name: "DEED_MARKETPLACE_APP_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
			{Name: "DEED_LOGGED_TIME_SERVICE_WEB_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
			{Name: "DEED_NONPROFIT_SERVICE_WEB_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
			{Name: "DEED_SLACK_SERVICE_WEB_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
			{Name: "DEED_TIME_OFF_SERVICE_WEB_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
		},
	)
	return definition
}

func simpleWebDefinition(
	id string,
	displayName string,
	kind ServiceKind,
	workspacePackage string,
	portEnvironment string,
	publishedRoutes []EnvironmentBinding,
) ServiceDefinition {
	return ServiceDefinition{
		ID:               id,
		DisplayName:      displayName,
		Kind:             kind,
		WorkspacePackage: workspacePackage,
		HasHTTPListener:  true,
		PortRequirements: []PortRequirement{{
			ID:                       "http",
			Purpose:                  "http",
			BindHost:                 "127.0.0.1",
			PreferredPortEnvironment: portEnvironment,
		}},
		PrepareCommands: []PlannedCommand{{
			Executable: RepositoryYarnExecutable,
			Arguments: []string{
				"turbo",
				"run",
				"build:no-dependencies",
				"--filter=" + workspacePackage,
				"--ui=stream",
			},
			WorkingDirectory: ".",
		}},
		RunCommand: PlannedCommand{
			Executable:       RepositoryYarnExecutable,
			Arguments:        []string{"workspace", workspacePackage, "start"},
			WorkingDirectory: ".",
		},
		EnvironmentBindings: []EnvironmentBinding{
			{Name: portEnvironment, PortRequirement: "http", Format: EnvironmentValueDecimalPort},
			{Name: "PORT", PortRequirement: "http", Format: EnvironmentValueDecimalPort},
		},
		PublishedRoutes: publishedRoutes,
		Readiness: []Probe{
			{Kind: ProbeKindTCP, PortRequirement: "http"},
		},
		Health: []Probe{{
			Kind:            ProbeKindHTTP,
			PortRequirement: "http",
			Method:          "GET",
			Path:            "/",
			AcceptedStatuses: []HTTPStatusRange{
				{Minimum: 200, Maximum: 499},
			},
		}},
	}
}

func organizerDefinition() ServiceDefinition {
	return ServiceDefinition{
		ID:               "organizer",
		DisplayName:      "Organizer",
		Kind:             ServiceKindWeb,
		WorkspacePackage: "organizer",
		HasHTTPListener:  true,
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
		PublishedRoutes: []EnvironmentBinding{
			{Name: "DEED_ORGANIZER_URI", PortRequirement: "http", Format: EnvironmentValueBrowserHTTPURL},
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

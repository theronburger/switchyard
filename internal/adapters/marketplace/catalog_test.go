package marketplace

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDefaultCatalogHasCompleteIsolatedMarketplaceFamily(t *testing.T) {
	want := []string{
		"api", "app", "auth-service", "company-service", "donation-batch-service", "donation-service",
		"email-service", "graph-service", "logged-time-service", "nonprofit-service", "notification-service",
		"opportunity-service", "organizer", "payroll-service", "report-service", "slack-service",
		"time-off-service", "wallet",
	}
	catalog := DefaultCatalog()
	if got := catalog.ServiceIDs(); !slices.Equal(got, want) {
		t.Fatalf("catalog services: got %v want %v", got, want)
	}
	for _, serviceID := range want {
		definition, found := catalog.Definition(serviceID)
		if !found {
			t.Fatalf("service %q is unavailable", serviceID)
		}
		if err := validateServiceDefinition(definition); err != nil {
			t.Fatalf("service %q is not a complete isolated definition: %v", serviceID, err)
		}
		for _, requirement := range definition.PortRequirements {
			if requirement.BindHost != "127.0.0.1" {
				t.Fatalf("service %q exposes %q outside literal loopback", serviceID, requirement.ID)
			}
		}
		for _, command := range append(append([]PlannedCommand(nil), definition.PrepareCommands...), definition.RunCommand) {
			joined := strings.Join(command.Arguments, "\x00")
			if command.Executable != RepositoryYarnExecutable || strings.Contains(joined, "docker") ||
				strings.Contains(joined, "start-changed") || strings.Contains(joined, "/bin/sh") {
				t.Fatalf("service %q has an unsafe or externally-owned command: %#v", serviceID, command)
			}
		}
		if definition.Kind == ServiceKindAPI && serviceID != "api" && definition.ServerlessOverlay == nil {
			t.Fatalf("Serverless service %q has no owned port projection", serviceID)
		}
		if serviceID != "auth-service" && !definition.HasHTTPListener {
			t.Fatalf("HTTP service %q is not marked as listening", serviceID)
		}
	}
	auth, _ := catalog.Definition("auth-service")
	if auth.HasHTTPListener || len(auth.PublishedRoutes) != 0 ||
		!reflect.DeepEqual(auth.Readiness, []Probe{{Kind: ProbeKindTCP, PortRequirement: "lambda"}}) ||
		!reflect.DeepEqual(auth.Health, []Probe{{Kind: ProbeKindTCP, PortRequirement: "lambda"}}) {
		t.Fatalf("Auth should expose only its Lambda listener: %#v", auth)
	}

	wallet, _ := catalog.Definition("wallet")
	if wallet.WorkspacePackage != "wallet-service" || wallet.ServerlessOverlay.Directory != "services/wallet" ||
		!slices.Contains(wallet.RunCommand.Arguments, "local-dev") {
		t.Fatalf("wallet exception was flattened incorrectly: %#v", wallet)
	}
	slack, _ := catalog.Definition("slack-service")
	if len(slack.Infrastructure) != 2 || len(slack.PrepareCommands) != 0 {
		t.Fatalf("Slack local dependencies are incomplete: %#v", slack)
	}
	report, _ := catalog.Definition("report-service")
	if !slices.Contains(report.ServerlessOverlay.Plugins, "serverless-offline-sqs") || len(report.Queues) != 2 {
		t.Fatalf("report queue consumer is not locally isolated: %#v", report)
	}
}

func TestCatalogPackageExceptions(t *testing.T) {
	if got, want := ServiceIDForPackage("wallet-service"), "wallet"; got != want {
		t.Fatalf("wallet service ID: got %q, want %q", got, want)
	}
	if got, want := PackageNameForServiceID("wallet"), "wallet-service"; got != want {
		t.Fatalf("wallet package: got %q, want %q", got, want)
	}
	if got, want := ServiceIDForPackage("nonprofit-service"), "nonprofit-service"; got != want {
		t.Fatalf("nonprofit service ID: got %q, want %q", got, want)
	}
	if got, want := PackageNameForServiceID("organizer"), "organizer"; got != want {
		t.Fatalf("organizer package: got %q, want %q", got, want)
	}
}

func TestCatalogDefinitionsAreIsolatedCopies(t *testing.T) {
	catalog := DefaultCatalog()
	first, found := catalog.Definition("organizer")
	if !found {
		t.Fatal("organizer definition is missing")
	}
	first.RunCommand.Arguments[0] = "changed"
	first.PortRequirements[0].ID = "changed"

	second, found := catalog.Definition("organizer")
	if !found {
		t.Fatal("organizer definition is missing")
	}
	if second.RunCommand.Arguments[0] != "workspace" {
		t.Fatal("catalog command was mutated through returned definition")
	}
	if second.PortRequirements[0].ID != "http" {
		t.Fatal("catalog port requirements were mutated through returned definition")
	}
}

func TestOrganizerAndNonprofitPlans(t *testing.T) {
	catalog := DefaultCatalog()
	plans, err := catalog.PlanEnvironment(
		[]string{"organizer", "nonprofit-service"},
		map[string]map[string]int{
			"organizer": {
				"http": 7005,
			},
			"nonprofit-service": {
				"http":           4019,
				"lambda":         5019,
				"elasticmq-rest": 19324,
				"elasticmq-ui":   19325,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("plan count: got %d, want 2", len(plans))
	}

	assertOrganizerPlan(t, plans[0])
	assertNonprofitPlan(t, plans[1])
}

func TestBrowserOriginsUseLocalhostWhileBindingsAndAPIRoutesUseLiteralLoopback(t *testing.T) {
	catalog := DefaultCatalog()
	organizer, found := catalog.Definition("organizer")
	if !found {
		t.Fatal("organizer definition is missing")
	}
	nonprofit, found := catalog.Definition("nonprofit-service")
	if !found {
		t.Fatal("nonprofit definition is missing")
	}
	if organizer.PortRequirements[0].BindHost != "127.0.0.1" ||
		nonprofit.PortRequirements[0].BindHost != "127.0.0.1" {
		t.Fatalf("service bindings escaped literal loopback: organizer=%#v nonprofit=%#v",
			organizer.PortRequirements, nonprofit.PortRequirements)
	}

	plans, err := catalog.PlanEnvironment(
		[]string{"organizer", "nonprofit-service"},
		map[string]map[string]int{
			"organizer":         {"http": 7002},
			"nonprofit-service": {"http": 4016, "lambda": 5016, "elasticmq-rest": 9324, "elasticmq-ui": 9325},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	for _, plan := range plans {
		for _, variable := range plan.Environment {
			values[variable.Name] = variable.Value
		}
	}
	if got := values["DEED_ORGANIZER_URI"]; got != "http://localhost:7002" {
		t.Fatalf("browser origin: got %q want %q", got, "http://localhost:7002")
	}
	for _, name := range []string{"DEED_NONPROFIT_API_URI", "DEED_NONPROFIT_SERVICE_URI"} {
		if got := values[name]; got != "http://127.0.0.1:4016" {
			t.Fatalf("service route %s: got %q want %q", name, got, "http://127.0.0.1:4016")
		}
	}
}

func TestPlanEnvironmentRequiresCompleteKnownPortAssignments(t *testing.T) {
	catalog := DefaultCatalog()
	tests := []struct {
		name  string
		ports map[string]int
	}{
		{
			name: "missing derived port",
			ports: map[string]int{
				"http":           4019,
				"elasticmq-rest": 19324,
				"elasticmq-ui":   19325,
			},
		},
		{
			name: "unknown port",
			ports: map[string]int{
				"http":           4019,
				"lambda":         5019,
				"elasticmq-rest": 19324,
				"elasticmq-ui":   19325,
				"mystery":        6000,
			},
		},
		{
			name: "invalid port",
			ports: map[string]int{
				"http":           4019,
				"lambda":         5019,
				"elasticmq-rest": 70000,
				"elasticmq-ui":   19325,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := catalog.PlanEnvironment(
				[]string{"nonprofit-service"},
				map[string]map[string]int{"nonprofit-service": test.ports},
			)
			if err == nil {
				t.Fatal("expected invalid assignments to fail")
			}
		})
	}
}

func TestPlanEnvironmentRejectsPortCollisions(t *testing.T) {
	catalog := DefaultCatalog()
	_, err := catalog.PlanEnvironment(
		[]string{"organizer", "nonprofit-service"},
		map[string]map[string]int{
			"organizer": {"http": 7005},
			"nonprofit-service": {
				"http":           7005,
				"lambda":         5019,
				"elasticmq-rest": 19324,
				"elasticmq-ui":   19325,
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected port conflict, got %v", err)
	}
}

func assertOrganizerPlan(t *testing.T, plan ServicePlan) {
	t.Helper()
	if plan.ID != "organizer" || plan.Kind != ServiceKindWeb || plan.WorkspacePackage != "organizer" {
		t.Fatalf("unexpected organizer identity: %#v", plan)
	}
	assertCommand(t, plan.RunCommand, PlannedCommand{
		Executable:       RepositoryYarnExecutable,
		Arguments:        []string{"workspace", "organizer", "start"},
		WorkingDirectory: ".",
	})
	assertNoShellOrPrototypeDependency(t, plan)

	wantPorts := []PortAssignment{{RequirementID: "http", Host: "127.0.0.1", Port: 7005}}
	if !reflect.DeepEqual(plan.Ports, wantPorts) {
		t.Fatalf("organizer ports: got %#v, want %#v", plan.Ports, wantPorts)
	}
	wantEnvironment := []EnvironmentVariable{
		{Name: "DEED_NONPROFIT_API_URI", Value: "http://127.0.0.1:4019"},
		{Name: "DEED_NONPROFIT_SERVICE_URI", Value: "http://127.0.0.1:4019"},
		{Name: "DEED_ORGANIZER_PORT", Value: "7005"},
		{Name: "DEED_ORGANIZER_URI", Value: "http://localhost:7005"},
		{Name: "PORT", Value: "7005"},
	}
	if !reflect.DeepEqual(plan.Environment, wantEnvironment) {
		t.Fatalf("organizer environment: got %#v, want %#v", plan.Environment, wantEnvironment)
	}
	if len(plan.Readiness) != 1 || plan.Readiness[0].Kind != ProbeKindTCP {
		t.Fatalf("organizer readiness: %#v", plan.Readiness)
	}
	if len(plan.Health) != 1 || plan.Health[0].Kind != ProbeKindHTTP || plan.Health[0].Path != "/" {
		t.Fatalf("organizer health: %#v", plan.Health)
	}
	if len(plan.Infrastructure) != 0 || plan.ServerlessOverlay != nil {
		t.Fatal("organizer unexpectedly requires infrastructure or a Serverless overlay")
	}
}

func assertNonprofitPlan(t *testing.T, plan ServicePlan) {
	t.Helper()
	if plan.ID != "nonprofit-service" || plan.Kind != ServiceKindAPI || plan.WorkspacePackage != "nonprofit-service" {
		t.Fatalf("unexpected nonprofit identity: %#v", plan)
	}
	assertCommand(t, plan.RunCommand, PlannedCommand{
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
	})
	assertNoShellOrPrototypeDependency(t, plan)

	wantPorts := []PortAssignment{
		{RequirementID: "http", Host: "127.0.0.1", Port: 4019},
		{RequirementID: "lambda", Host: "127.0.0.1", Port: 5019},
		{RequirementID: "elasticmq-rest", Host: "127.0.0.1", Port: 19324},
		{RequirementID: "elasticmq-ui", Host: "127.0.0.1", Port: 19325},
	}
	if !reflect.DeepEqual(plan.Ports, wantPorts) {
		t.Fatalf("nonprofit ports: got %#v, want %#v", plan.Ports, wantPorts)
	}
	wantEnvironment := []EnvironmentVariable{
		{Name: "DEED_NONPROFIT_API_URI", Value: "http://127.0.0.1:4019"},
		{Name: "DEED_NONPROFIT_SERVICE_PORT", Value: "4019"},
		{Name: "DEED_NONPROFIT_SERVICE_URI", Value: "http://127.0.0.1:4019"},
		{Name: "DEED_ORGANIZER_URI", Value: "http://localhost:7005"},
	}
	if !reflect.DeepEqual(plan.Environment, wantEnvironment) {
		t.Fatalf("nonprofit environment: got %#v, want %#v", plan.Environment, wantEnvironment)
	}

	if len(plan.Readiness) != 2 || plan.Readiness[0].PortRequirement != "http" || plan.Readiness[1].PortRequirement != "lambda" {
		t.Fatalf("nonprofit readiness: %#v", plan.Readiness)
	}
	if len(plan.Health) != 1 || plan.Health[0].AcceptedStatuses[0] != (HTTPStatusRange{Minimum: 200, Maximum: 499}) {
		t.Fatalf("nonprofit health: %#v", plan.Health)
	}

	if len(plan.Infrastructure) != 1 {
		t.Fatalf("nonprofit infrastructure: %#v", plan.Infrastructure)
	}
	elasticMQ := plan.Infrastructure[0]
	if elasticMQ.ID != "elasticmq" || elasticMQ.Scope != EnvironmentInfrastructureScope || !elasticMQ.Dedicated {
		t.Fatalf("ElasticMQ is not isolated per environment: %#v", elasticMQ)
	}
	wantContainerPorts := []ContainerPort{
		{PortRequirement: "elasticmq-rest", ContainerPort: 9324},
		{PortRequirement: "elasticmq-ui", ContainerPort: 9325},
	}
	if !reflect.DeepEqual(elasticMQ.Ports, wantContainerPorts) {
		t.Fatalf("ElasticMQ ports: got %#v, want %#v", elasticMQ.Ports, wantContainerPorts)
	}

	overlay := plan.ServerlessOverlay
	if overlay == nil {
		t.Fatal("nonprofit Serverless overlay is missing")
	}
	if overlay.Directory != "services/nonprofit-service" ||
		overlay.Filename != ".switchyard.serverless.ts" ||
		overlay.SourceConfig != "serverless.ts" {
		t.Fatalf("unexpected Serverless overlay identity: %#v", overlay)
	}
	wantOverrides := []ServerlessOverride{
		{
			ConfigurationPath: []string{"custom", "serverless-offline", "host"},
			PortRequirement:   "http",
			Format:            OverlayValueLoopback,
		},
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
	}
	if !reflect.DeepEqual(overlay.Overrides, wantOverrides) {
		t.Fatalf("Serverless overrides: got %#v, want %#v", overlay.Overrides, wantOverrides)
	}
}

func assertCommand(t *testing.T, got PlannedCommand, want PlannedCommand) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command: got %#v, want %#v", got, want)
	}
}

func assertNoShellOrPrototypeDependency(t *testing.T, plan ServicePlan) {
	t.Helper()
	commands := append(append([]PlannedCommand{}, plan.PrepareCommands...), plan.RunCommand)
	for _, command := range commands {
		if command.Executable != RepositoryYarnExecutable {
			t.Fatalf("unexpected executable %q", command.Executable)
		}
		for _, argument := range command.Arguments {
			if argument == "bash" || argument == "sh" || strings.Contains(argument, "start-changed") {
				t.Fatalf("unsafe or prototype-dependent command argument %q", argument)
			}
		}
	}
}

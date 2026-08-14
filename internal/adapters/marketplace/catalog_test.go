package marketplace

import (
	"reflect"
	"strings"
	"testing"
)

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
		{Name: "DEED_ORGANIZER_PORT", Value: "7005"},
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

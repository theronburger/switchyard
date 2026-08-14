package marketplacecontrol

import (
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

func TestEnvironmentRegistryIsValidatedAndImmutable(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "marketplace")
	registrations := []EnvironmentRegistration{testRegistration("env_one", root)}
	registry, err := NewEnvironmentRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	registrations[0].WorktreeRoot = "/foreign"
	registration, err := registry.Lookup("env_one")
	if err != nil {
		t.Fatal(err)
	}
	if registration.WorktreeRoot != root {
		t.Fatalf("registry changed through constructor input: %#v", registration)
	}

	invalid := []EnvironmentRegistration{
		testRegistration("../hostile", root),
		testRegistration("env_two", "relative/path"),
		testRegistration("env_three", root),
	}
	invalid[2].YarnCJS = filepath.Join(filepath.Dir(root), "foreign", "yarn.cjs")
	for _, candidate := range invalid {
		if _, err := NewEnvironmentRegistry([]EnvironmentRegistration{candidate}); !errors.Is(err, ErrRegistryInvalid) {
			t.Fatalf("invalid registration was accepted: %#v, err=%v", candidate, err)
		}
	}
	if _, err := registry.Lookup("unknown"); !errors.Is(err, ErrEnvironmentUnknown) {
		t.Fatalf("unknown lookup: %v", err)
	}
	alias := testRegistration("env_alias", root)
	if _, err := NewEnvironmentRegistry([]EnvironmentRegistration{
		testRegistration("env_one", root), alias,
	}); !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("two live environments used one worktree: %v", err)
	}
}

func TestPlanBuilderUsesAssignedPortsExactArgvAndIsolatedIdentity(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	registration := testRegistration("env_one", filepath.Join(base, "tree one"))
	registry := mustRegistry(t, registration)
	builder, err := NewDefaultPlanBuilder(registry)
	if err != nil {
		t.Fatal(err)
	}
	leases := testLeases("env_one", 17101, 17201, 17301, 17302, 17401)
	plan, err := builder.Build(environment.PlanningRequest{
		EnvironmentID: "env_one",
		RunID:         "run_one",
		Intent: environment.PlanIntent{
			Adapter:    marketplaceAdapterID,
			ServiceIDs: []string{"organizer", "nonprofit-service"},
		},
		AssignedPorts: leases,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Projection == nil || plan.Projection.ID != marketplaceServerlessProjection {
		t.Fatalf("projection identity: %#v", plan.Projection)
	}
	if len(plan.Preparations) != 2 {
		t.Fatalf("preparation count: %#v", plan.Preparations)
	}
	if len(plan.Initializations) != 12 {
		t.Fatalf("initialization count: %#v", plan.Initializations)
	}
	wantEndpoint := "http://127.0.0.1:17301"
	wantReadiness := elasticMQReadinessArguments(wantEndpoint)
	for index, initialization := range plan.Initializations {
		if initialization.Executable != marketplaceCurlExecutable ||
			initialization.Directory != registration.WorktreeRoot ||
			strings.Contains(strings.Join(initialization.Arguments, "\x00"), "start-changed") ||
			!strings.Contains(
				initialization.RunDirectory,
				string(filepath.Separator)+"initializations"+string(filepath.Separator),
			) ||
			!reflect.DeepEqual(initialization.Environment, []string{
				"HOME=/Users/test",
				"PATH=/opt/switchyard/node/bin:/opt/homebrew/bin:/usr/bin:/bin",
				"TMPDIR=/tmp/switchyard",
			}) {
			t.Fatalf("unsafe initialization %d: %#v", index, initialization)
		}
		if index == 0 {
			if initialization.ID != "nonprofit-service.initialize.elasticmq.readiness" ||
				initialization.Timeout != elasticMQReadinessTimeout ||
				!reflect.DeepEqual(initialization.Arguments, wantReadiness) {
				t.Fatalf("bounded ElasticMQ readiness: %#v", initialization)
			}
			continue
		}
		queueIndex := index - 1
		if initialization.ID != "nonprofit-service.initialize.elasticmq.queue."+strconv.Itoa(queueIndex) ||
			initialization.Timeout != elasticMQCreateQueueTimeout ||
			!reflect.DeepEqual(
				initialization.Arguments,
				elasticMQCreateQueueArguments(wantEndpoint, elasticMQQueueNames[queueIndex]),
			) {
			t.Fatalf("CreateQueue initialization %d: %#v", queueIndex, initialization)
		}
	}
	preparations := make(map[string]environment.PreparationSpec, len(plan.Preparations))
	for _, preparation := range plan.Preparations {
		preparations[preparation.ID] = preparation
		if preparation.Executable != registration.NodeExecutable ||
			preparation.Directory != registration.WorktreeRoot ||
			preparation.Timeout != marketplacePreparationTimeout ||
			strings.Contains(strings.Join(preparation.Arguments, "\x00"), "start-changed") ||
			!strings.Contains(preparation.RunDirectory, string(filepath.Separator)+"preparations"+string(filepath.Separator)) ||
			strings.Contains(preparation.RunDirectory, string(filepath.Separator)+"services"+string(filepath.Separator)) {
			t.Fatalf("unsafe preparation: %#v", preparation)
		}
	}
	if !reflect.DeepEqual(preparations["organizer.prepare.0"].Arguments, []string{
		registration.YarnCJS, "turbo", "run", "build:no-dependencies", "--filter=organizer", "--ui=stream",
	}) || !reflect.DeepEqual(preparations["nonprofit-service.prepare.0"].Arguments, []string{
		registration.YarnCJS, "turbo", "run", "build:no-dependencies",
		"--filter=nonprofit-service", "--ui=stream",
	}) {
		t.Fatalf("preparation exact argv: %#v", plan.Preparations)
	}
	organizer := serviceLaunch(t, plan, "organizer")
	nonprofit := serviceLaunch(t, plan, "nonprofit-service")
	if organizer.Process.Executable != registration.NodeExecutable ||
		!reflect.DeepEqual(organizer.Process.Arguments, []string{
			registration.YarnCJS, "workspace", "organizer", "start",
		}) {
		t.Fatalf("organizer exact launch: %#v", organizer.Process)
	}
	if nonprofit.Process.Executable != registration.NodeExecutable ||
		!reflect.DeepEqual(nonprofit.Process.Arguments, []string{
			registration.YarnCJS,
			"workspace", "nonprofit-service", "sls:withEnv", "offline", "start",
			"--stage", "local", "--config", ".switchyard.serverless.ts",
		}) {
		t.Fatalf("nonprofit exact launch: %#v", nonprofit.Process)
	}
	for _, launch := range plan.Services {
		if launch.Process.Directory != registration.WorktreeRoot ||
			strings.Contains(strings.Join(launch.Process.Arguments, "\x00"), "start-changed") ||
			launch.Process.Executable == "/bin/sh" || launch.Process.Executable == "/bin/bash" {
			t.Fatalf("unsafe service launch: %#v", launch)
		}
		wantRunDirectory := filepath.Join(
			registration.RunRoot, "environments", "env_one", "runs", "run_one", "services", launch.ID,
		)
		if launch.Process.RunDirectory != wantRunDirectory {
			t.Fatalf("run directory: got %q want %q", launch.Process.RunDirectory, wantRunDirectory)
		}
		if launch.Readiness.ID != readinessID(launch.ID) {
			t.Fatalf("readiness ID: %#v", launch.Readiness)
		}
	}
	if !reflect.DeepEqual(organizer.Process.Environment, []string{
		"HOME=/Users/test",
		"PATH=/opt/switchyard/node/bin:/opt/homebrew/bin:/usr/bin:/bin",
		"TMPDIR=/tmp/switchyard",
		"DEED_NONPROFIT_API_URI=http://127.0.0.1:17101",
		"DEED_ORGANIZER_PORT=17401",
		"PORT=17401",
	}) {
		t.Fatalf("organizer assigned environment: %#v", organizer.Process.Environment)
	}
	if !reflect.DeepEqual(nonprofit.Process.Environment, []string{
		"HOME=/Users/test",
		"PATH=/opt/switchyard/node/bin:/opt/homebrew/bin:/usr/bin:/bin",
		"TMPDIR=/tmp/switchyard",
		"DEED_NONPROFIT_API_URI=http://127.0.0.1:17101",
		"DEED_NONPROFIT_SERVICE_PORT=17101",
	}) {
		t.Fatalf("nonprofit assigned environment: %#v", nonprofit.Process.Environment)
	}
	if !reflect.DeepEqual(preparations["organizer.prepare.0"].Environment, organizer.Process.Environment) ||
		!reflect.DeepEqual(preparations["nonprofit-service.prepare.0"].Environment, nonprofit.Process.Environment) {
		t.Fatalf("preparation environment is not controlled: %#v", plan.Preparations)
	}
	if !reflect.DeepEqual(nonprofit.PortKeys, []portlease.Key{
		{EnvironmentID: "env_one", ServiceID: "nonprofit-service", Purpose: "http"},
		{EnvironmentID: "env_one", ServiceID: "nonprofit-service", Purpose: "lambda"},
	}) {
		t.Fatalf("service port ownership includes infrastructure: %#v", nonprofit.PortKeys)
	}
	if len(plan.Infrastructure) != 1 {
		t.Fatalf("infrastructure goals: %#v", plan.Infrastructure)
	}
	goal := plan.Infrastructure[0]
	if goal.Name == "demo-elasticmq" || !strings.HasPrefix(goal.Name, "switchyard-elasticmq-") ||
		goal.Identity.EnvironmentID != "env_one" || goal.Identity.RunID != "run_one" ||
		goal.Identity.ServiceID != "nonprofit-service.elasticmq" ||
		goal.Identity.InstanceID != registration.DaemonInstanceID {
		t.Fatalf("ElasticMQ is not isolated and labelled: %#v", goal)
	}
	if !reflect.DeepEqual(goal.PortBindings, []containerhost.PortBinding{
		{Host: "127.0.0.1", HostPort: 17301, ContainerPort: 9324, Protocol: containerhost.PortProtocolTCP},
		{Host: "127.0.0.1", HostPort: 17302, ContainerPort: 9325, Protocol: containerhost.PortProtocolTCP},
	}) {
		t.Fatalf("ElasticMQ assigned bindings: %#v", goal.PortBindings)
	}
	containerPlan, err := (containerhost.Planner{DockerBinary: "/opt/homebrew/bin/docker"}).Build(
		containerhost.Inventory{Resources: []containerhost.Resource{{
			Kind: containerhost.ResourceContainer, ID: "foreign-id", Name: "demo-elasticmq",
			Image: "softwaremill/elasticmq", State: "running", Running: true,
			Labels: map[string]string{"team": "marketplace"},
		}}},
		plan.Infrastructure,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(containerPlan.Actions) != 2 || len(containerPlan.Protections) != 0 {
		t.Fatalf("isolated infrastructure plan: %#v", containerPlan)
	}
	for _, action := range containerPlan.Actions {
		if action.ResourceName == "demo-elasticmq" {
			t.Fatalf("foreign demo-elasticmq was targeted: %#v", action)
		}
	}
	create := containerPlan.Actions[0]
	if create.Kind != containerhost.ActionCreate ||
		!reflect.DeepEqual(create.PortBindings, goal.PortBindings) ||
		create.Command.Executable != "/opt/homebrew/bin/docker" ||
		!containsArgumentPair(create.Command.Arguments, "--publish", "127.0.0.1:17301:9324/tcp") ||
		!containsArgumentPair(create.Command.Arguments, "--publish", "127.0.0.1:17302:9325/tcp") {
		t.Fatalf("exact isolated create command: %#v", create)
	}
}

func TestPlanBuilderKeepsTwoEnvironmentsDistinct(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	first := testRegistration("env_first", filepath.Join(base, "first"))
	second := testRegistration("env_second", filepath.Join(base, "second"))
	builder, err := NewDefaultPlanBuilder(mustRegistry(t, first, second))
	if err != nil {
		t.Fatal(err)
	}
	build := func(registration EnvironmentRegistration, httpPort int) environment.ExecutionPlan {
		t.Helper()
		plan, err := builder.Build(environment.PlanningRequest{
			EnvironmentID: registration.EnvironmentID,
			RunID:         "run_shared",
			Intent: environment.PlanIntent{
				Adapter:    marketplaceAdapterID,
				ServiceIDs: []string{"nonprofit-service", "organizer"},
			},
			AssignedPorts: testLeases(
				registration.EnvironmentID,
				httpPort, httpPort+1000, httpPort+2000, httpPort+2001, httpPort+3000,
			),
		})
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	firstPlan := build(first, 18001)
	secondPlan := build(second, 19001)
	firstOrganizer := serviceLaunch(t, firstPlan, "organizer")
	secondOrganizer := serviceLaunch(t, secondPlan, "organizer")
	if reflect.DeepEqual(firstOrganizer.Process.Environment, secondOrganizer.Process.Environment) ||
		firstOrganizer.Process.Directory == secondOrganizer.Process.Directory ||
		firstOrganizer.Process.RunDirectory == secondOrganizer.Process.RunDirectory ||
		firstPlan.Infrastructure[0].Name == secondPlan.Infrastructure[0].Name {
		t.Fatalf("environment routing collided:\nfirst=%#v\nsecond=%#v", firstPlan, secondPlan)
	}
	if reflect.DeepEqual(firstPlan.Infrastructure[0].PortBindings, secondPlan.Infrastructure[0].PortBindings) {
		t.Fatalf("infrastructure bindings collided:\nfirst=%#v\nsecond=%#v", firstPlan, secondPlan)
	}
	if reflect.DeepEqual(firstPlan.Preparations[0].RunDirectory, secondPlan.Preparations[0].RunDirectory) {
		t.Fatalf("preparation run directories collided:\nfirst=%#v\nsecond=%#v", firstPlan, secondPlan)
	}
	if len(firstPlan.Initializations) != 12 || len(secondPlan.Initializations) != 12 ||
		reflect.DeepEqual(firstPlan.Initializations[0].Arguments, secondPlan.Initializations[0].Arguments) ||
		firstPlan.Initializations[0].RunDirectory == secondPlan.Initializations[0].RunDirectory ||
		!containsArgument(firstPlan.Initializations[0].Arguments, "http://127.0.0.1:20001") ||
		!containsArgument(secondPlan.Initializations[0].Arguments, "http://127.0.0.1:21001") {
		t.Fatalf("initialization routing collided:\nfirst=%#v\nsecond=%#v", firstPlan, secondPlan)
	}
}

func TestElasticMQQueueNamesAreStrictAndCurated(t *testing.T) {
	t.Parallel()
	want := []string{
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
	if !reflect.DeepEqual(elasticMQQueueNames, want) || !validElasticMQQueueNames(elasticMQQueueNames) {
		t.Fatalf("curated queue set: %#v", elasticMQQueueNames)
	}
	for _, invalid := range [][]string{
		append([]string(nil), want[:10]...),
		func() []string {
			duplicate := append([]string(nil), want...)
			duplicate[len(duplicate)-1] = duplicate[0]
			return duplicate
		}(),
		func() []string {
			hostile := append([]string(nil), want...)
			hostile[len(hostile)-1] = "foreign/queue"
			return hostile
		}(),
	} {
		if validElasticMQQueueNames(invalid) {
			t.Fatalf("invalid queue set was accepted: %#v", invalid)
		}
	}
}

func TestPlanBuilderRejectsInvalidAdapterServiceAndLeaseSets(t *testing.T) {
	t.Parallel()
	registration := testRegistration("env_validate", filepath.Join(t.TempDir(), "marketplace"))
	builder, err := NewDefaultPlanBuilder(mustRegistry(t, registration))
	if err != nil {
		t.Fatal(err)
	}
	valid := environment.PlanningRequest{
		EnvironmentID: registration.EnvironmentID,
		RunID:         "run_validate",
		Intent: environment.PlanIntent{
			Adapter:    marketplaceAdapterID,
			ServiceIDs: []string{"organizer"},
		},
		AssignedPorts: []portlease.Lease{{
			Key: portlease.Key{
				EnvironmentID: registration.EnvironmentID,
				ServiceID:     "organizer",
				Purpose:       "http",
			},
			Host: "127.0.0.1", Port: 19501,
		}},
	}
	organizerPlan, err := builder.Build(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(organizerPlan.Initializations) != 0 {
		t.Fatalf("organizer-only plan initialized infrastructure: %#v", organizerPlan.Initializations)
	}
	tests := map[string]func(*environment.PlanningRequest){
		"adapter": func(request *environment.PlanningRequest) {
			request.Intent.Adapter = "foreign"
		},
		"duplicate service": func(request *environment.PlanningRequest) {
			request.Intent.ServiceIDs = append(request.Intent.ServiceIDs, "organizer")
		},
		"unknown service lease": func(request *environment.PlanningRequest) {
			request.AssignedPorts = append(request.AssignedPorts, portlease.Lease{
				Key: portlease.Key{
					EnvironmentID: registration.EnvironmentID,
					ServiceID:     "foreign-service",
					Purpose:       "http",
				},
				Host: "127.0.0.1", Port: 19502,
			})
		},
		"reused host port": func(request *environment.PlanningRequest) {
			request.Intent.ServiceIDs = append(request.Intent.ServiceIDs, "nonprofit-service")
			request.AssignedPorts = testLeases(registration.EnvironmentID, 19501, 20501, 21501, 21502, 19501)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := valid
			request.Intent.ServiceIDs = append([]string(nil), valid.Intent.ServiceIDs...)
			request.AssignedPorts = append([]portlease.Lease(nil), valid.AssignedPorts...)
			mutate(&request)
			if _, err := builder.Build(request); !errors.Is(err, ErrMarketplacePlanInvalid) {
				t.Fatalf("invalid plan input was accepted: %v", err)
			}
		})
	}
}

func containsArgumentPair(arguments []string, first, second string) bool {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == first && arguments[index+1] == second {
			return true
		}
	}
	return false
}

func containsArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
			return true
		}
	}
	return false
}

type secretFailingExecutionCatalog struct {
	marketplaceadapter.Catalog
}

func (catalog secretFailingExecutionCatalog) PlanEnvironment(
	[]string,
	map[string]map[string]int,
) ([]marketplaceadapter.ServicePlan, error) {
	return nil, errors.New("AWS_SECRET_ACCESS_KEY=secret token@example.invalid")
}

func TestPlanBuilderRejectsHostileInputsWithoutSecretOutput(t *testing.T) {
	t.Parallel()
	registration := testRegistration("env_safe", filepath.Join(t.TempDir(), "tree"))
	builder, err := NewPlanBuilder(
		mustRegistry(t, registration),
		secretFailingExecutionCatalog{Catalog: marketplaceadapter.DefaultCatalog()},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.Build(environment.PlanningRequest{
		EnvironmentID: registration.EnvironmentID,
		RunID:         "run_safe",
		Intent: environment.PlanIntent{
			Adapter:    marketplaceAdapterID,
			ServiceIDs: []string{"organizer"},
		},
		AssignedPorts: []portlease.Lease{{
			Key:  portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "organizer", Purpose: "http"},
			Host: "127.0.0.1", Port: 17001,
		}},
	})
	if !errors.Is(err, ErrMarketplacePlanInvalid) || strings.ContainsAny(err.Error(), "@=") ||
		strings.Contains(strings.ToLower(err.Error()), "secret") || strings.Contains(strings.ToLower(err.Error()), "token") {
		t.Fatalf("planner leaked a sensitive catalog error: %v", err)
	}

	_, err = builder.Build(environment.PlanningRequest{
		EnvironmentID: registration.EnvironmentID,
		RunID:         "../../credential",
		Intent:        environment.PlanIntent{Adapter: marketplaceAdapterID, ServiceIDs: []string{"organizer"}},
	})
	if !errors.Is(err, ErrMarketplacePlanInvalid) || strings.Contains(err.Error(), "credential") {
		t.Fatalf("hostile run ID error leaked input: %v", err)
	}
}

func testRegistration(environmentID, worktreeRoot string) EnvironmentRegistration {
	return EnvironmentRegistration{
		EnvironmentID:      environmentID,
		WorktreeRoot:       worktreeRoot,
		NodeExecutable:     "/opt/switchyard/node/bin/node",
		YarnCJS:            filepath.Join(worktreeRoot, ".yarn", "releases", "yarn-3.2.4.cjs"),
		RunRoot:            filepath.Join(filepath.Dir(worktreeRoot), "switchyard-runs"),
		HomeDirectory:      "/Users/test",
		TemporaryDirectory: "/tmp/switchyard",
		ExecutablePath:     "/opt/switchyard/node/bin:/opt/homebrew/bin:/usr/bin:/bin",
		DaemonInstanceID:   "daemon_instance_1",
	}
}

func mustRegistry(t *testing.T, registrations ...EnvironmentRegistration) EnvironmentRegistry {
	t.Helper()
	registry, err := NewEnvironmentRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testLeases(
	environmentID string,
	nonprofitHTTP int,
	nonprofitLambda int,
	elasticREST int,
	elasticUI int,
	organizerHTTP int,
) []portlease.Lease {
	lease := func(serviceID, purpose string, port int) portlease.Lease {
		return portlease.Lease{
			Key:  portlease.Key{EnvironmentID: environmentID, ServiceID: serviceID, Purpose: purpose},
			Host: "127.0.0.1", Port: port,
		}
	}
	return []portlease.Lease{
		lease("organizer", "http", organizerHTTP),
		lease("nonprofit-service", "elasticmq-ui", elasticUI),
		lease("nonprofit-service", "http", nonprofitHTTP),
		lease("nonprofit-service", "elasticmq-rest", elasticREST),
		lease("nonprofit-service", "lambda", nonprofitLambda),
	}
}

func serviceLaunch(
	t *testing.T,
	plan environment.ExecutionPlan,
	serviceID string,
) environment.ServiceLaunch {
	t.Helper()
	for _, service := range plan.Services {
		if service.ID == serviceID {
			return service
		}
	}
	t.Fatalf("service %q is missing from %#v", serviceID, plan.Services)
	return environment.ServiceLaunch{}
}

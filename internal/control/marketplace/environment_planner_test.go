package marketplacecontrol

import (
	"errors"
	"strings"
	"testing"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
)

type failingCatalog struct{}

func (failingCatalog) PlanEnvironment(
	[]string,
	map[string]map[string]int,
) ([]marketplaceadapter.ServicePlan, error) {
	return nil, errors.New("AWS_SECRET_ACCESS_KEY=secret https://token@example.invalid")
}

func TestDefaultEnvironmentPlannerPreservesLocalMarketplacePlan(t *testing.T) {
	planner := NewDefaultEnvironmentPlanner()
	result := planner.Plan(EnvironmentPlanRequest{
		ServiceIDs: []string{"organizer", "nonprofit-service"},
		AssignedPorts: map[string]map[string]int{
			"organizer": {"http": 7005},
			"nonprofit-service": {
				"http":           4019,
				"lambda":         5019,
				"elasticmq-rest": 19324,
				"elasticmq-ui":   19325,
			},
		},
	})
	if len(result.Errors) != 0 || result.Adapter != "marketplace" || len(result.Services) != 2 {
		t.Fatalf("planning result: %#v", result)
	}
	for _, service := range result.Services {
		for _, port := range service.Ports {
			if port.Host != "127.0.0.1" {
				t.Fatalf("service %q planned a non-loopback port: %#v", service.ID, port)
			}
		}
		commands := append(append([]marketplaceadapter.PlannedCommand(nil), service.PrepareCommands...), service.RunCommand)
		for _, command := range commands {
			for _, argument := range command.Arguments {
				if strings.Contains(argument, "start-changed") {
					t.Fatalf("service %q depends on start-changed: %#v", service.ID, command)
				}
			}
		}
	}
	if infrastructure := result.Services[1].Infrastructure; len(infrastructure) != 1 ||
		!infrastructure[0].Dedicated {
		t.Fatalf("nonprofit infrastructure is not isolated: %#v", infrastructure)
	}
}

func TestEnvironmentPlannerReturnsStructuredSecretFreeError(t *testing.T) {
	planner, err := NewEnvironmentPlanner(failingCatalog{})
	if err != nil {
		t.Fatal(err)
	}
	result := planner.Plan(EnvironmentPlanRequest{ServiceIDs: []string{"secret-service-name"}})
	if len(result.Errors) != 1 || result.Errors[0].Code != "MARKETPLACE_ENVIRONMENT_PLAN_INVALID" {
		t.Fatalf("planning errors: %#v", result.Errors)
	}
	serialized := result.Errors[0].Error()
	if strings.Contains(serialized, "secret") || strings.Contains(serialized, "token") ||
		strings.Contains(serialized, "example.invalid") {
		t.Fatalf("planner error exposed sensitive data: %q", serialized)
	}
}

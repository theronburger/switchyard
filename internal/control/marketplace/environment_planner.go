package marketplacecontrol

import (
	"fmt"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
)

const (
	PlanningErrorInvalidRequest     = "MARKETPLACE_ENVIRONMENT_PLAN_INVALID"
	PlanningErrorPlannerUnavailable = "MARKETPLACE_ENVIRONMENT_PLANNER_UNAVAILABLE"
)

type CatalogPlanner interface {
	PlanEnvironment([]string, map[string]map[string]int) ([]marketplaceadapter.ServicePlan, error)
}

type EnvironmentPlanner struct {
	catalog CatalogPlanner
}

type EnvironmentPlanRequest struct {
	ServiceIDs    []string
	AssignedPorts map[string]map[string]int
}

type EnvironmentPlanResult struct {
	Adapter  string
	Services []marketplaceadapter.ServicePlan
	Errors   []PlanningError
}

type PlanningError struct {
	Code      string
	Message   string
	Retryable bool
}

func (planningError PlanningError) Error() string {
	return planningError.Message
}

func NewEnvironmentPlanner(catalog CatalogPlanner) (EnvironmentPlanner, error) {
	if catalog == nil {
		return EnvironmentPlanner{}, fmt.Errorf("Marketplace environment planner requires a catalog")
	}
	return EnvironmentPlanner{catalog: catalog}, nil
}

func NewDefaultEnvironmentPlanner() EnvironmentPlanner {
	return EnvironmentPlanner{catalog: marketplaceadapter.DefaultCatalog()}
}

func (planner EnvironmentPlanner) Plan(request EnvironmentPlanRequest) EnvironmentPlanResult {
	if planner.catalog == nil {
		return failedPlanningResult(
			PlanningErrorPlannerUnavailable,
			"The Marketplace service catalog is unavailable.",
		)
	}
	services, err := planner.catalog.PlanEnvironment(request.ServiceIDs, request.AssignedPorts)
	if err != nil {
		return failedPlanningResult(
			PlanningErrorInvalidRequest,
			"Marketplace could not produce a service plan from the requested services and port assignments.",
		)
	}
	return EnvironmentPlanResult{Adapter: "marketplace", Services: services}
}

func failedPlanningResult(code string, message string) EnvironmentPlanResult {
	return EnvironmentPlanResult{
		Adapter: "marketplace",
		Errors: []PlanningError{{
			Code:      code,
			Message:   message,
			Retryable: false,
		}},
	}
}

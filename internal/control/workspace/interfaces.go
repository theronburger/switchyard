package workspace

import "context"

type PlanBuilder interface {
	Build(PlanningRequest) (Plan, error)
}

type StepRunner interface {
	Run(context.Context, StepSpec) error
}

type RequirementVerifier interface {
	Verify(context.Context, Plan) error
}

type Journal interface {
	Begin(context.Context, OperationRecord) error
	Update(context.Context, OperationRecord) error
	Publish(context.Context, OperationRecord, Result) error
	Current(context.Context, string) (Result, bool, error)
	Incomplete(context.Context) ([]OperationRecord, error)
}

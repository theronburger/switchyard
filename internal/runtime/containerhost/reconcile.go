package containerhost

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrPlanExpired         = errors.New("container reconciliation plan expired")
	ErrOwnershipUnverified = errors.New("container resource ownership could not be verified")
	ErrPlanInvalid         = errors.New("container reconciliation plan is invalid")
)

type Reconciler struct {
	Runner       Runner
	Resources    ResourceReader
	DockerBinary string
}

func (reconciler Reconciler) Apply(ctx context.Context, plan Plan) error {
	if reconciler.Runner == nil || reconciler.Resources == nil {
		return errors.New("container reconciler dependencies are required")
	}
	if plan.SchemaVersion != PlanSchemaVersion || plan.BaseRevision == "" {
		return ErrPlanInvalid
	}
	dockerBinary := reconciler.DockerBinary
	if dockerBinary == "" {
		dockerBinary = "docker"
	}
	for _, action := range plan.Actions {
		if err := validateAction(dockerBinary, action); err != nil {
			return err
		}
	}

	current, err := reconciler.Resources.Inventory(ctx)
	if err != nil {
		return err
	}
	canonicalCurrent, err := NewInventory(current.Resources)
	if err != nil {
		return err
	}
	if canonicalCurrent.Revision != plan.BaseRevision {
		return ErrPlanExpired
	}

	for _, action := range plan.Actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if action.Kind == ActionCreate {
			if resourceCollision(canonicalCurrent, action) {
				return ErrPlanExpired
			}
		} else {
			if err := reconciler.verifyActionTarget(ctx, action); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := reconciler.Runner.Run(ctx, action.Command.Clone()); err != nil {
			return redactRunnerError(action.Command, err)
		}
	}
	return nil
}

func (reconciler Reconciler) verifyActionTarget(ctx context.Context, action Action) error {
	reference := actionReference(action)
	resource, err := reconciler.Resources.Inspect(ctx, action.ResourceKind, reference)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return contextError
		}
		return ErrOwnershipUnverified
	}
	ownership, identity := ClassifyLabels(resource.Labels)
	if ownership != OwnershipOwned || identity != action.Identity ||
		resource.Kind != action.ResourceKind {
		return ErrOwnershipUnverified
	}
	if action.ResourceID != "" && resource.ID != action.ResourceID {
		return ErrOwnershipUnverified
	}
	if action.ResourceID == "" && resource.Name != action.ResourceName {
		return ErrOwnershipUnverified
	}
	if action.ResourceKind == ResourceContainer {
		configured, err := canonicalPortBindings(resource.PortBindings)
		if err != nil || resource.Image != action.Image ||
			!portBindingsEqual(configured, action.PortBindings) {
			return ErrOwnershipUnverified
		}
		if resource.Running {
			published, err := canonicalPortBindings(resource.PublishedPortBindings)
			if err != nil || !portBindingsEqual(published, action.PortBindings) {
				return ErrOwnershipUnverified
			}
		}
	}
	return nil
}

func validateAction(dockerBinary string, action Action) error {
	if !action.ResourceKind.Valid() || !resourceNamePattern.MatchString(action.ResourceName) ||
		action.Identity.Validate() != nil {
		return ErrPlanInvalid
	}
	if action.ResourceKind == ResourceContainer {
		if !safeImageReference(action.Image) {
			return ErrPlanInvalid
		}
		canonical, err := canonicalPortBindings(action.PortBindings)
		if err != nil || !portBindingsEqual(canonical, action.PortBindings) {
			return ErrPlanInvalid
		}
	} else if action.Image != "" || len(action.PortBindings) != 0 {
		return ErrPlanInvalid
	}
	switch action.Kind {
	case ActionCreate:
		if action.ResourceID != "" {
			return ErrPlanInvalid
		}
	case ActionStart, ActionStop:
		if action.ResourceKind != ResourceContainer {
			return ErrPlanInvalid
		}
	case ActionRemove:
	default:
		return ErrPlanInvalid
	}
	if (action.Kind == ActionStop || action.Kind == ActionRemove) && action.ResourceID == "" {
		return ErrPlanInvalid
	}
	expected := expectedCommand(dockerBinary, action)
	if action.Command.Executable != expected.Executable ||
		!slices.Equal(action.Command.Arguments, expected.Arguments) {
		return ErrPlanInvalid
	}
	return nil
}

func resourceCollision(inventory Inventory, action Action) bool {
	for _, resource := range inventory.Resources {
		if resource.Kind != action.ResourceKind {
			continue
		}
		if resource.Name == action.ResourceName {
			return true
		}
		if resource.Ownership == OwnershipOwned && resource.Identity == action.Identity {
			return true
		}
	}
	return false
}

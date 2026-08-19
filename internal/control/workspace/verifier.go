package workspace

import (
	"context"
	"os"
)

type OSRequirementVerifier struct{}

func (OSRequirementVerifier) Verify(ctx context.Context, plan Plan) error {
	for _, requirement := range plan.Requirements {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(requirement.Path)
		if err != nil {
			return ErrNotReady
		}
		switch requirement.Kind {
		case RequirementDirectory:
			if !info.IsDir() {
				return ErrNotReady
			}
		case RequirementRegularFile:
			if !info.Mode().IsRegular() {
				return ErrNotReady
			}
		case RequirementExecutable:
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return ErrNotReady
			}
		default:
			return ErrInvalidPlan
		}
	}
	return nil
}

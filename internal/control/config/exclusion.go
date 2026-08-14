package config

import (
	"fmt"
	"path/filepath"

	"github.com/theronburger/switchyard/internal/control/inventory"
)

type LocalManifestExclusionPlan struct {
	SharedExcludePath string
	Pattern           string
}

func PlanLocalManifestExclusion(
	controlPaths inventory.RepositoryControlPaths,
) (LocalManifestExclusionPlan, error) {
	commonDirectory := filepath.Clean(controlPaths.CommonDirectory)
	sharedExcludePath := filepath.Clean(controlPaths.SharedExcludePath)
	if !filepath.IsAbs(commonDirectory) || !filepath.IsAbs(sharedExcludePath) ||
		sharedExcludePath != filepath.Join(commonDirectory, "info", "exclude") {
		return LocalManifestExclusionPlan{}, fmt.Errorf(
			"local manifest exclusion requires the shared Git common info/exclude path",
		)
	}
	return LocalManifestExclusionPlan{
		SharedExcludePath: sharedExcludePath,
		Pattern:           LocalManifestExcludePattern,
	}, nil
}

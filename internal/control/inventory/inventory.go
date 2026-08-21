package inventory

import (
	"context"
	"fmt"
	"sort"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

type Inventory struct {
	repositoryReader RepositoryReader
}

type DiscoveryResult struct {
	Repository   *contractv2.Repository
	ControlPaths RepositoryControlPaths
	Alerts       []Alert
	Errors       []DiscoveryError
}

type RepositoryControlPaths struct {
	CommonDirectory   string
	SharedExcludePath string
}

func New(repositoryReader RepositoryReader) (Inventory, error) {
	if repositoryReader == nil {
		return Inventory{}, fmt.Errorf("repository reader is required")
	}
	return Inventory{repositoryReader: repositoryReader}, nil
}

func (inventory Inventory) DiscoverRepository(
	ctx context.Context,
	repositoryRoot string,
) DiscoveryResult {
	if repositoryRoot == "" {
		return DiscoveryResult{Errors: []DiscoveryError{newDiscoveryError(
			ErrorProfileObservationInvalid,
			"repository",
			"",
		)}}
	}

	observation := inventory.repositoryReader.ReadRepository(ctx, repositoryRoot)
	repository, worktreeIDs, valid := projectRepository(observation)
	result := DiscoveryResult{}
	if valid {
		result.Repository = &repository
		result.ControlPaths = RepositoryControlPaths{
			CommonDirectory:   observation.CommonDirectory,
			SharedExcludePath: observation.SharedExcludePath,
		}
	}

	repositoryIdentity := ""
	if result.Repository != nil {
		repositoryIdentity = result.Repository.ID
	}
	for _, observedError := range observation.Errors {
		resourceKind, resourceID := observedResource(
			repositoryIdentity,
			worktreeIDs,
			observedError.WorktreePath,
		)
		result.Errors = append(result.Errors, newDiscoveryError(
			observedError.Code,
			resourceKind,
			resourceID,
		))
	}
	if !valid && len(result.Errors) == 0 {
		result.Errors = append(result.Errors, newDiscoveryError(
			ErrorProfileObservationInvalid,
			"repository",
			repositoryIdentity,
		))
	}
	for _, observedAlert := range observation.Alerts {
		if result.Repository == nil {
			continue
		}
		severity, summary, known := alertDetails(observedAlert.Code)
		if !known {
			result.Errors = append(result.Errors, newDiscoveryError(
				ErrorProfileObservationInvalid,
				"repository",
				repositoryIdentity,
			))
			continue
		}
		worktreeIdentity := worktreeIDs[observedAlert.WorktreePath]
		result.Alerts = append(result.Alerts, Alert{
			ID:           alertID(repositoryIdentity, worktreeIdentity, observedAlert.Code),
			Code:         observedAlert.Code,
			Severity:     severity,
			Summary:      summary,
			RepositoryID: repositoryIdentity,
			WorktreeID:   worktreeIdentity,
		})
	}

	sort.Slice(result.Alerts, func(left, right int) bool {
		return result.Alerts[left].ID < result.Alerts[right].ID
	})
	sort.Slice(result.Errors, func(left, right int) bool {
		if result.Errors[left].Code != result.Errors[right].Code {
			return result.Errors[left].Code < result.Errors[right].Code
		}
		return result.Errors[left].ResourceID < result.Errors[right].ResourceID
	})
	return result
}

func observedResource(
	repositoryID string,
	worktreeIDs map[string]string,
	worktreePath string,
) (resourceKind string, resourceID string) {
	if worktreePath != "" {
		return "worktree", worktreeIDs[worktreePath]
	}
	return "repository", repositoryID
}

func newDiscoveryError(code ErrorCode, resourceKind string, resourceID string) DiscoveryError {
	message, retryable, known := errorDetails(code)
	if !known {
		message, retryable, _ = errorDetails(ErrorProfileObservationInvalid)
		code = ErrorProfileObservationInvalid
	}
	return DiscoveryError{
		Code:         code,
		Message:      message,
		Retryable:    retryable,
		ResourceKind: resourceKind,
		ResourceID:   resourceID,
	}
}

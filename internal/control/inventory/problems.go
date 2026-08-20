package inventory

type AlertCode string

const (
	AlertWorktreePrunable AlertCode = "WORKTREE_PRUNABLE"
	AlertWorktreeBare     AlertCode = "WORKTREE_BARE"
)

type ErrorCode string

const (
	ErrorRepositoryGitPathsUnavailable  ErrorCode = "REPOSITORY_GIT_PATHS_UNAVAILABLE"
	ErrorRepositoryRemoteUnavailable    ErrorCode = "REPOSITORY_REMOTE_UNAVAILABLE"
	ErrorRepositoryWorktreesUnavailable ErrorCode = "REPOSITORY_WORKTREES_UNAVAILABLE"
	ErrorWorktreeIdentityUnavailable    ErrorCode = "WORKTREE_IDENTITY_UNAVAILABLE"
	ErrorProfileObservationInvalid      ErrorCode = "PROFILE_OBSERVATION_INVALID"
)

type Alert struct {
	ID           string
	Code         AlertCode
	Severity     string
	Summary      string
	RepositoryID string
	WorktreeID   string
}

type DiscoveryError struct {
	Code         ErrorCode
	Message      string
	Retryable    bool
	ResourceKind string
	ResourceID   string
}

func (discoveryError DiscoveryError) Error() string {
	return discoveryError.Message
}

func alertDetails(code AlertCode) (severity string, summary string, known bool) {
	switch code {
	case AlertWorktreePrunable:
		return "warning", "Git reports this worktree as prunable.", true
	case AlertWorktreeBare:
		return "warning", "This bare worktree cannot host a local runtime environment.", true
	default:
		return "", "", false
	}
}

func errorDetails(code ErrorCode) (message string, retryable bool, known bool) {
	switch code {
	case ErrorRepositoryGitPathsUnavailable:
		return "Git repository control paths are unavailable.", true, true
	case ErrorRepositoryRemoteUnavailable:
		return "The repository remote identity is unavailable.", true, true
	case ErrorRepositoryWorktreesUnavailable:
		return "Registered Git worktrees are unavailable.", true, true
	case ErrorWorktreeIdentityUnavailable:
		return "A worktree administrative identity is unavailable; a path-scoped fallback was used.", true, true
	case ErrorProfileObservationInvalid:
		return "The repository profile observation was invalid.", false, true
	default:
		return "", false, false
	}
}

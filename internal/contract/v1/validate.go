package contractv1

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var workspaceFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const (
	maximumOpaqueIDBytes       = 256
	maximumIdempotencyKeyBytes = 512
	maximumRequestedServices   = 32
	maximumLineCount           = int64(1 << 50)
	maximumChangedFiles        = 1_000_000
	maximumPullRequestChecks   = 128
	maximumDisplayTextBytes    = 2_048
	maximumCleanupPathBytes    = 8_192
)

func (snapshot StatusSnapshot) Validate() error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version: got %d, want %d", snapshot.SchemaVersion, SchemaVersion)
	}
	if snapshot.SnapshotRevision < 0 {
		return fmt.Errorf("snapshot revision must not be negative")
	}
	if snapshot.Daemon.InstanceID == "" {
		return fmt.Errorf("daemon instance id is required")
	}
	if snapshot.Repositories == nil || snapshot.Environments == nil ||
		snapshot.Operations == nil || snapshot.Alerts == nil {
		return fmt.Errorf("status snapshot collections must be JSON arrays, not null")
	}

	repositories := make(map[string]Repository, len(snapshot.Repositories))
	worktrees := make(map[string]Worktree)
	for _, repository := range snapshot.Repositories {
		if repository.ID == "" {
			return fmt.Errorf("repository id is required")
		}
		if _, exists := repositories[repository.ID]; exists {
			return fmt.Errorf("duplicate repository id %q", repository.ID)
		}
		if repository.Worktrees == nil {
			return fmt.Errorf("repository %q worktrees must be a JSON array, not null", repository.ID)
		}
		if err := validateRepositoryObservation(repository); err != nil {
			return err
		}
		repositories[repository.ID] = repository
		if repository.Runtime != nil {
			if err := validateRepositoryRuntime(repository.ID, *repository.Runtime); err != nil {
				return err
			}
		}
		for _, worktree := range repository.Worktrees {
			if worktree.ID == "" {
				return fmt.Errorf("worktree id is required in repository %q", repository.ID)
			}
			if _, exists := worktrees[worktree.ID]; exists {
				return fmt.Errorf("duplicate worktree id %q", worktree.ID)
			}
			if err := validateWorktreeChanges(repository, worktree); err != nil {
				return err
			}
			if err := validatePullRequestObservation(worktree); err != nil {
				return err
			}
			if err := validateWorkspaceStatus(worktree); err != nil {
				return err
			}
			worktrees[worktree.ID] = worktree
		}
	}

	environments := make(map[string]Environment, len(snapshot.Environments))
	for _, environment := range snapshot.Environments {
		if environment.ID == "" {
			return fmt.Errorf("environment id is required")
		}
		if _, exists := environments[environment.ID]; exists {
			return fmt.Errorf("duplicate environment id %q", environment.ID)
		}
		if _, exists := repositories[environment.RepositoryID]; !exists {
			return fmt.Errorf("environment %q references unknown repository %q", environment.ID, environment.RepositoryID)
		}
		if _, exists := worktrees[environment.WorktreeID]; !exists {
			return fmt.Errorf("environment %q references unknown worktree %q", environment.ID, environment.WorktreeID)
		}
		if environment.Revision < 0 {
			return fmt.Errorf("environment %q revision must not be negative", environment.ID)
		}
		if environment.TargetID != "" {
			repository := repositories[environment.RepositoryID]
			if repository.Runtime == nil || !runtimeContainsTarget(*repository.Runtime, environment.TargetID) {
				return fmt.Errorf("environment %q references unknown runtime target %q", environment.ID, environment.TargetID)
			}
		}
		if environment.Services == nil || environment.PortLeases == nil ||
			environment.InfrastructureLeases == nil || environment.AttentionAlertIDs == nil {
			return fmt.Errorf("environment %q collections must be JSON arrays, not null", environment.ID)
		}
		if environment.URLs == nil {
			return fmt.Errorf("environment %q urls must be a JSON object, not null", environment.ID)
		}
		if err := validateEnvironment(environment); err != nil {
			return err
		}
		environments[environment.ID] = environment
	}

	alerts := make(map[string]Alert, len(snapshot.Alerts))
	for _, alert := range snapshot.Alerts {
		if alert.ID == "" {
			return fmt.Errorf("alert id is required")
		}
		if _, exists := alerts[alert.ID]; exists {
			return fmt.Errorf("duplicate alert id %q", alert.ID)
		}
		if alert.EnvironmentID != "" {
			if _, exists := environments[alert.EnvironmentID]; !exists {
				return fmt.Errorf("alert %q references unknown environment %q", alert.ID, alert.EnvironmentID)
			}
		}
		alerts[alert.ID] = alert
	}

	for _, environment := range snapshot.Environments {
		for _, alertID := range environment.AttentionAlertIDs {
			if _, exists := alerts[alertID]; !exists {
				return fmt.Errorf("environment %q references unknown alert %q", environment.ID, alertID)
			}
		}
	}

	return nil
}

func validateRepositoryObservation(repository Repository) error {
	observation := repository.Observation
	if observation == nil {
		return nil
	}
	if observation.LastAttemptAt.IsZero() {
		return fmt.Errorf("repository %q observation has no attempt time", repository.ID)
	}
	if observation.ObservedAt == nil {
		if !observation.Stale || observation.ErrorCode == "" {
			return fmt.Errorf("repository %q unavailable observation is inconsistent", repository.ID)
		}
	} else {
		if observation.LastAttemptAt.Before(*observation.ObservedAt) {
			return fmt.Errorf("repository %q attempt predates its observation", repository.ID)
		}
		if observation.Stale != (observation.ErrorCode != "") {
			return fmt.Errorf("repository %q observation staleness is inconsistent", repository.ID)
		}
	}
	if observation.ErrorCode != "" && !validOpaqueValue(observation.ErrorCode, maximumOpaqueIDBytes) {
		return fmt.Errorf("repository %q observation error code is invalid", repository.ID)
	}
	return nil
}

func validateWorkspaceStatus(worktree Worktree) error {
	status := worktree.Workspace
	if status == nil {
		return nil
	}
	if (status.Ownership != "adopted" && status.Ownership != "managed") ||
		(status.State != "unprepared" && status.State != "ready") ||
		status.Toolchains == nil || len(status.Toolchains) > 16 {
		return fmt.Errorf("worktree %q workspace status is invalid", worktree.ID)
	}
	if status.State == "ready" && (!workspaceFingerprintPattern.MatchString(status.Fingerprint) || status.PreparedAt.IsZero()) {
		return fmt.Errorf("worktree %q ready workspace status is incomplete", worktree.ID)
	}
	if status.State == "unprepared" && (status.Fingerprint != "" || !status.PreparedAt.IsZero() || len(status.Toolchains) != 0) {
		return fmt.Errorf("worktree %q unprepared workspace status is inconsistent", worktree.ID)
	}
	seen := make(map[string]struct{}, len(status.Toolchains))
	for _, toolchain := range status.Toolchains {
		if !validOpaqueValue(toolchain.ID, maximumOpaqueIDBytes) || toolchain.RequestedVersion == "" ||
			toolchain.ResolvedVersion == "" || len(toolchain.RequestedVersion) > maximumOpaqueIDBytes ||
			len(toolchain.ResolvedVersion) > maximumOpaqueIDBytes {
			return fmt.Errorf("worktree %q workspace toolchain is invalid", worktree.ID)
		}
		if _, duplicate := seen[toolchain.ID]; duplicate {
			return fmt.Errorf("worktree %q workspace toolchain is duplicated", worktree.ID)
		}
		seen[toolchain.ID] = struct{}{}
	}
	return nil
}

func validatePullRequestObservation(worktree Worktree) error {
	observation := worktree.PullRequest
	if observation == nil {
		return nil
	}
	if observation.LastAttemptAt.IsZero() ||
		(observation.Account != "" && !validOpaqueValue(observation.Account, maximumOpaqueIDBytes)) {
		return fmt.Errorf("worktree %q pull request observation is incomplete", worktree.ID)
	}
	switch observation.Status {
	case "found":
		if observation.ObservedAt == nil || observation.PullRequest == nil {
			return fmt.Errorf("worktree %q found pull request is incomplete", worktree.ID)
		}
	case "none":
		if observation.ObservedAt == nil || observation.PullRequest != nil {
			return fmt.Errorf("worktree %q empty pull request observation is inconsistent", worktree.ID)
		}
	case "unavailable":
		if observation.ObservedAt != nil || observation.PullRequest != nil || observation.Stale || observation.ErrorCode == "" {
			return fmt.Errorf("worktree %q unavailable pull request observation is inconsistent", worktree.ID)
		}
	default:
		return fmt.Errorf("worktree %q pull request observation status is invalid", worktree.ID)
	}
	if observation.Stale && observation.ErrorCode == "" {
		return fmt.Errorf("worktree %q stale pull request observation has no reason", worktree.ID)
	}
	if !observation.Stale && observation.Status != "unavailable" && observation.ErrorCode != "" {
		return fmt.Errorf("worktree %q current pull request observation has a stale reason", worktree.ID)
	}
	if observation.ErrorCode != "" && !validOpaqueValue(observation.ErrorCode, maximumOpaqueIDBytes) {
		return fmt.Errorf("worktree %q pull request error code is invalid", worktree.ID)
	}
	if observation.ObservedAt != nil && observation.LastAttemptAt.Before(*observation.ObservedAt) {
		return fmt.Errorf("worktree %q pull request attempt predates its observation", worktree.ID)
	}
	if observation.PullRequest != nil {
		if err := validatePullRequest(worktree, *observation.PullRequest); err != nil {
			return err
		}
	}
	return nil
}

func validatePullRequest(worktree Worktree, pullRequest PullRequest) error {
	if pullRequest.Number < 1 ||
		!validOpaqueValue(pullRequest.Title, maximumDisplayTextBytes) ||
		!validHTTPSURL(pullRequest.URL) ||
		!validOpaqueValue(pullRequest.BaseBranch, maximumDisplayTextBytes) ||
		!validOpaqueValue(pullRequest.HeadBranch, maximumDisplayTextBytes) ||
		!isContractGitObjectID(pullRequest.HeadRevision) ||
		pullRequest.CreatedAt.IsZero() || pullRequest.UpdatedAt.IsZero() ||
		pullRequest.UpdatedAt.Before(pullRequest.CreatedAt) {
		return fmt.Errorf("worktree %q pull request metadata is invalid", worktree.ID)
	}
	if !oneOf(pullRequest.State, "open", "closed", "merged") ||
		!oneOf(pullRequest.Mergeable, "mergeable", "conflicting", "unknown", "not_applicable") ||
		!oneOf(pullRequest.MergeState, "clean", "blocked", "behind", "dirty", "has_hooks", "unstable", "unknown", "not_applicable") ||
		!oneOf(pullRequest.ReviewDecision, "approved", "changes_requested", "review_required", "unknown", "not_applicable") {
		return fmt.Errorf("worktree %q pull request state is invalid", worktree.ID)
	}
	if pullRequest.State == "merged" && pullRequest.MergedAt == nil {
		return fmt.Errorf("worktree %q merged pull request has no merge time", worktree.ID)
	}
	if pullRequest.Checks.Items == nil || len(pullRequest.Checks.Items) > maximumPullRequestChecks ||
		!oneOf(pullRequest.Checks.State, "passing", "failing", "pending", "cancelled", "neutral", "none", "unavailable") {
		return fmt.Errorf("worktree %q pull request checks are invalid", worktree.ID)
	}
	counts := pullRequest.Checks.Passing + pullRequest.Checks.Failing + pullRequest.Checks.Pending +
		pullRequest.Checks.Skipping + pullRequest.Checks.Cancelled
	if pullRequest.Checks.Total < 0 || pullRequest.Checks.Passing < 0 || pullRequest.Checks.Failing < 0 ||
		pullRequest.Checks.Pending < 0 || pullRequest.Checks.Skipping < 0 || pullRequest.Checks.Cancelled < 0 ||
		pullRequest.Checks.Total != len(pullRequest.Checks.Items) || counts != pullRequest.Checks.Total ||
		!pullRequestCheckStateMatchesCounts(pullRequest.Checks) {
		return fmt.Errorf("worktree %q pull request check totals are inconsistent", worktree.ID)
	}
	for _, check := range pullRequest.Checks.Items {
		if !validOpaqueValue(check.Name, maximumDisplayTextBytes) ||
			(check.Workflow != "" && !validOpaqueValue(check.Workflow, maximumDisplayTextBytes)) ||
			!validOpaqueValue(check.State, maximumOpaqueIDBytes) ||
			!oneOf(check.Bucket, "pass", "fail", "pending", "skipping", "cancel") ||
			(check.URL != "" && !validHTTPSURL(check.URL)) {
			return fmt.Errorf("worktree %q pull request check is invalid", worktree.ID)
		}
	}
	return nil
}

func pullRequestCheckStateMatchesCounts(checks PullRequestChecks) bool {
	switch checks.State {
	case "failing":
		return checks.Failing > 0
	case "pending":
		return checks.Failing == 0 && checks.Pending > 0
	case "cancelled":
		return checks.Failing == 0 && checks.Pending == 0 && checks.Cancelled > 0
	case "passing":
		return checks.Failing == 0 && checks.Pending == 0 && checks.Cancelled == 0 && checks.Passing > 0
	case "neutral":
		return checks.Failing == 0 && checks.Pending == 0 && checks.Cancelled == 0 &&
			checks.Passing == 0 && checks.Skipping > 0
	case "none", "unavailable":
		return checks.Total == 0
	default:
		return false
	}
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.Fragment == "" && len(value) <= maximumDisplayTextBytes
}

func validateWorktreeChanges(repository Repository, worktree Worktree) error {
	if worktree.Changes == nil {
		return nil
	}
	changes := *worktree.Changes
	if !isContractGitObjectID(changes.BaseRevision) || changes.Services == nil {
		return fmt.Errorf("worktree %q line changes are incomplete", worktree.ID)
	}
	for _, summary := range []LineChanges{
		changes.Committed, changes.Uncommitted, changes.SharedCommitted, changes.SharedUncommitted,
	} {
		if !validLineChanges(summary) {
			return fmt.Errorf("worktree %q line changes must not be negative", worktree.ID)
		}
	}
	knownServices := map[string]struct{}{}
	if repository.Runtime != nil {
		for _, service := range repository.Runtime.Services {
			knownServices[service.ID] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(changes.Services))
	for _, service := range changes.Services {
		if _, known := knownServices[service.ServiceID]; !known {
			return fmt.Errorf("worktree %q changes reference unknown service %q", worktree.ID, service.ServiceID)
		}
		if _, duplicate := seen[service.ServiceID]; duplicate {
			return fmt.Errorf("worktree %q changes duplicate service %q", worktree.ID, service.ServiceID)
		}
		seen[service.ServiceID] = struct{}{}
		for _, summary := range []LineChanges{service.Committed, service.Uncommitted} {
			if !validLineChanges(summary) {
				return fmt.Errorf("worktree %q service changes must not be negative", worktree.ID)
			}
		}
	}
	if !lineChangeTotalsMatch(changes.Committed, changes.SharedCommitted, changes.Services, true) ||
		!lineChangeTotalsMatch(changes.Uncommitted, changes.SharedUncommitted, changes.Services, false) {
		return fmt.Errorf("worktree %q line change totals do not match service attribution", worktree.ID)
	}
	hasUncommittedFiles := changes.Uncommitted.Files > 0
	if hasUncommittedFiles != (worktree.Git.HasTrackedChanges || worktree.Git.HasUntrackedFiles) {
		return fmt.Errorf("worktree %q Git state does not match uncommitted line changes", worktree.ID)
	}
	return nil
}

func validLineChanges(changes LineChanges) bool {
	return changes.Additions >= 0 && changes.Additions <= maximumLineCount &&
		changes.Deletions >= 0 && changes.Deletions <= maximumLineCount &&
		changes.Files >= 0 && changes.Files <= maximumChangedFiles
}

func lineChangeTotalsMatch(
	total LineChanges,
	shared LineChanges,
	services []ServiceLineChanges,
	committed bool,
) bool {
	attributed := shared
	for _, service := range services {
		changes := service.Uncommitted
		if committed {
			changes = service.Committed
		}
		attributed.Additions += changes.Additions
		attributed.Deletions += changes.Deletions
		attributed.Files += changes.Files
	}
	return attributed == total
}

func isContractGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func runtimeContainsTarget(runtime RepositoryRuntime, targetID string) bool {
	for _, target := range runtime.Targets {
		if target.ID == targetID {
			return true
		}
	}
	return false
}

func validateRepositoryRuntime(repositoryID string, runtime RepositoryRuntime) error {
	if runtime.DefaultTargetID == "" || runtime.Targets == nil || len(runtime.Targets) == 0 ||
		runtime.Services == nil || len(runtime.Services) == 0 {
		return fmt.Errorf("repository %q runtime catalog is incomplete", repositoryID)
	}
	targets := make(map[string]struct{}, len(runtime.Targets))
	for _, target := range runtime.Targets {
		if !validOpaqueValue(target.ID, maximumOpaqueIDBytes) || target.DisplayName == "" || target.Risk == "" {
			return fmt.Errorf("repository %q runtime target is invalid", repositoryID)
		}
		if _, duplicate := targets[target.ID]; duplicate {
			return fmt.Errorf("repository %q runtime target %q is duplicated", repositoryID, target.ID)
		}
		targets[target.ID] = struct{}{}
	}
	if _, found := targets[runtime.DefaultTargetID]; !found {
		return fmt.Errorf("repository %q runtime default target is unknown", repositoryID)
	}
	services := make(map[string]struct{}, len(runtime.Services))
	for _, service := range runtime.Services {
		if !validOpaqueValue(service.ID, maximumOpaqueIDBytes) || service.DisplayName == "" || service.Kind == "" {
			return fmt.Errorf("repository %q runtime service is invalid", repositoryID)
		}
		if _, duplicate := services[service.ID]; duplicate {
			return fmt.Errorf("repository %q runtime service %q is duplicated", repositoryID, service.ID)
		}
		if service.Available && service.UnavailableReason != "" {
			return fmt.Errorf("repository %q available runtime service has an unavailable reason", repositoryID)
		}
		if !service.Available && service.UnavailableReason == "" {
			return fmt.Errorf("repository %q unavailable runtime service has no reason", repositoryID)
		}
		services[service.ID] = struct{}{}
	}
	return nil
}

func validateEnvironment(environment Environment) error {
	if !oneOf(environment.DesiredState, "unknown", "stopped", "running", "failed", "orphaned") ||
		!oneOf(environment.ObservedState, "unknown", "stopped", "starting", "running", "stopping", "exited", "failed", "orphaned", "degraded") ||
		!oneOf(environment.Health, "unknown", "not_applicable", "starting", "healthy", "degraded", "unhealthy") {
		return fmt.Errorf("environment %q lifecycle state is invalid", environment.ID)
	}
	services := make(map[string]Service, len(environment.Services))
	for _, service := range environment.Services {
		if service.ID == "" {
			return fmt.Errorf("service id is required in environment %q", environment.ID)
		}
		if _, exists := services[service.ID]; exists {
			return fmt.Errorf("duplicate service id %q in environment %q", service.ID, environment.ID)
		}
		if service.PortLeaseIDs == nil {
			return fmt.Errorf("service %q port leases must be a JSON array, not null", service.ID)
		}
		if !oneOf(service.DesiredState, "unknown", "stopped", "running", "failed", "orphaned") ||
			!oneOf(service.ObservedState, "unknown", "stopped", "starting", "running", "stopping", "exited", "failed", "orphaned", "degraded", "unverifiable") ||
			!oneOf(service.Health, "unknown", "not_applicable", "starting", "healthy", "degraded", "unhealthy") ||
			(service.ObservationCode != "" && !validOpaqueValue(service.ObservationCode, maximumOpaqueIDBytes)) {
			return fmt.Errorf("service %q lifecycle state is invalid", service.ID)
		}
		if service.Run != nil {
			if !validOpaqueValue(service.Run.ID, maximumOpaqueIDBytes) || service.Run.StartedAt.IsZero() ||
				service.Run.RestartCount < 0 || service.Run.ProcessCount < 0 || service.Run.CPUPercent < 0 ||
				service.Run.CPUPercent > 100 || service.Run.MemoryBytes < 0 {
				return fmt.Errorf("service %q run is invalid", service.ID)
			}
			hasSource := service.Run.SourceRevision != "" || !service.Run.SourceObservedAt.IsZero() ||
				service.Run.SourceHasTrackedChanges || service.Run.SourceHasUntrackedFiles
			if hasSource && (!isContractGitObjectID(service.Run.SourceRevision) || service.Run.SourceObservedAt.IsZero()) {
				return fmt.Errorf("service %q run source is incomplete", service.ID)
			}
		}
		services[service.ID] = service
	}

	leases := make(map[string]PortLease, len(environment.PortLeases))
	for _, lease := range environment.PortLeases {
		if lease.ID == "" {
			return fmt.Errorf("port lease id is required in environment %q", environment.ID)
		}
		if _, exists := leases[lease.ID]; exists {
			return fmt.Errorf("duplicate port lease id %q in environment %q", lease.ID, environment.ID)
		}
		if _, exists := services[lease.ServiceID]; !exists {
			return fmt.Errorf("port lease %q references unknown service %q", lease.ID, lease.ServiceID)
		}
		if lease.Port < 1 || lease.Port > 65535 {
			return fmt.Errorf("port lease %q has invalid port %d", lease.ID, lease.Port)
		}
		leases[lease.ID] = lease
	}

	for _, service := range environment.Services {
		for _, leaseID := range service.PortLeaseIDs {
			if _, exists := leases[leaseID]; !exists {
				return fmt.Errorf("service %q references unknown port lease %q", service.ID, leaseID)
			}
		}
	}

	return nil
}

func (request MutationRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version: got %d, want %d", request.SchemaVersion, SchemaVersion)
	}
	if !validOpaqueValue(request.RequestID, maximumOpaqueIDBytes) {
		return fmt.Errorf("request id is invalid")
	}
	if !validOpaqueValue(request.IdempotencyKey, maximumIdempotencyKeyBytes) {
		return fmt.Errorf("idempotency key is invalid")
	}
	if request.ExpectedEnvironmentRevision != nil && *request.ExpectedEnvironmentRevision < 0 {
		return fmt.Errorf("expected environment revision must not be negative")
	}
	return nil
}

func (request StartEnvironmentRequest) Validate() error {
	if err := request.MutationRequest.Validate(); err != nil {
		return err
	}
	if !validOpaqueValue(request.WorktreeID, maximumOpaqueIDBytes) {
		return fmt.Errorf("worktree id is invalid")
	}
	if request.TargetID != "" && !validOpaqueValue(request.TargetID, maximumOpaqueIDBytes) {
		return fmt.Errorf("target id is invalid")
	}
	if request.ConfirmedTargetID != "" && !validOpaqueValue(request.ConfirmedTargetID, maximumOpaqueIDBytes) {
		return fmt.Errorf("confirmed target id is invalid")
	}
	if request.TargetID != "" && request.ConfirmedTargetID != "" && request.TargetID != request.ConfirmedTargetID {
		return fmt.Errorf("confirmed target id does not match target id")
	}
	if len(request.ServiceIDs) == 0 ||
		len(request.ServiceIDs) > maximumRequestedServices {
		return fmt.Errorf("service ids must be a non-empty bounded JSON array")
	}
	seen := make(map[string]struct{}, len(request.ServiceIDs))
	for _, serviceID := range request.ServiceIDs {
		if !validOpaqueValue(serviceID, maximumOpaqueIDBytes) {
			return fmt.Errorf("service id is invalid")
		}
		if _, duplicate := seen[serviceID]; duplicate {
			return fmt.Errorf("duplicate service id %q", serviceID)
		}
		seen[serviceID] = struct{}{}
	}
	return nil
}

func (request StopEnvironmentRequest) Validate() error {
	return request.MutationRequest.Validate()
}

func (request CreateWorktreeRequest) Validate() error {
	if err := request.MutationRequest.Validate(); err != nil {
		return err
	}
	if request.ExpectedEnvironmentRevision != nil ||
		!validOpaqueValue(request.RepositoryID, maximumOpaqueIDBytes) ||
		!validOpaqueValue(request.Branch, maximumOpaqueIDBytes) ||
		(request.StartPoint != "" && !validOpaqueValue(request.StartPoint, maximumOpaqueIDBytes)) {
		return fmt.Errorf("worktree creation request is invalid")
	}
	return nil
}

func (request ArchiveWorktreeRequest) Validate() error {
	if err := request.MutationRequest.Validate(); err != nil {
		return err
	}
	if request.ExpectedEnvironmentRevision != nil || !validOpaqueValue(request.WorktreeID, maximumOpaqueIDBytes) {
		return fmt.Errorf("worktree archive request is invalid")
	}
	return nil
}

func (request AdoptWorktreeRequest) Validate() error {
	if err := request.MutationRequest.Validate(); err != nil {
		return err
	}
	if request.ExpectedEnvironmentRevision != nil || !validOpaqueValue(request.WorktreeID, maximumOpaqueIDBytes) {
		return fmt.Errorf("worktree adoption request is invalid")
	}
	return nil
}

func (request PrepareWorktreeRequest) Validate() error {
	if err := request.MutationRequest.Validate(); err != nil {
		return err
	}
	if request.ExpectedEnvironmentRevision != nil || !validOpaqueValue(request.WorktreeID, maximumOpaqueIDBytes) {
		return fmt.Errorf("worktree preparation request is invalid")
	}
	return nil
}

func (request ConfigurationValidationRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion || request.ExpectedRevision < 0 {
		return fmt.Errorf("configuration validation request is invalid")
	}
	return nil
}

func (request ConfigurationAcceptanceRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion || request.ExpectedRevision < 0 ||
		!validDigest(request.Digest) {
		return fmt.Errorf("configuration acceptance request is invalid")
	}
	return nil
}

func (candidate ConfigurationCandidate) Validate() error {
	if candidate.SchemaVersion != SchemaVersion || !validDigest(candidate.Digest) ||
		!validDigest(candidate.SourceDigest) || candidate.CompilerVersion == "" ||
		candidate.RepositoryDigests == nil || candidate.ExecutableDigests == nil || candidate.StagedAt.IsZero() {
		return fmt.Errorf("configuration candidate is invalid")
	}
	for path, digest := range candidate.ExecutableDigests {
		if path == "" || len(path) > maximumCleanupPathBytes || !validDigest(digest) {
			return fmt.Errorf("configuration executable digest is invalid")
		}
	}
	for key, digest := range candidate.RepositoryDigests {
		if !validOpaqueValue(key, maximumOpaqueIDBytes) || !validDigest(digest) {
			return fmt.Errorf("configuration repository digest is invalid")
		}
	}
	return nil
}

func (status ConfigurationStatus) Validate() error {
	if status.SchemaVersion != SchemaVersion || status.AcceptedRevision < 0 {
		return fmt.Errorf("configuration status is invalid")
	}
	switch status.State {
	case "missing", "accepted", "pending":
	default:
		return fmt.Errorf("configuration status state is invalid")
	}
	if status.AcceptedRevision == 0 && status.AcceptedDigest != "" ||
		status.AcceptedRevision > 0 && !validDigest(status.AcceptedDigest) {
		return fmt.Errorf("accepted configuration identity is invalid")
	}
	if status.Candidate != nil && status.Candidate.Validate() != nil {
		return fmt.Errorf("configuration status candidate is invalid")
	}
	if status.State == "pending" && status.Candidate == nil {
		return fmt.Errorf("pending configuration candidate is required")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func (request CleanupPlanRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion || !validCleanupScope(request.Scope) {
		return fmt.Errorf("cleanup plan request is invalid")
	}
	return nil
}

func (request CleanupApplyRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion || !validOpaqueValue(request.PlanID, maximumOpaqueIDBytes) ||
		request.ExpectedRevision < 1 || request.CandidateIDs == nil || len(request.CandidateIDs) > 1024 {
		return fmt.Errorf("cleanup apply request is invalid")
	}
	seen := make(map[string]struct{}, len(request.CandidateIDs))
	for _, id := range request.CandidateIDs {
		if !validOpaqueValue(id, maximumOpaqueIDBytes) {
			return fmt.Errorf("cleanup candidate id is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("cleanup candidate id is duplicated")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validCleanupScope(scope CleanupScope) bool {
	switch scope.Kind {
	case "global":
		return scope.ID == ""
	case "repository", "worktree":
		return validOpaqueValue(scope.ID, maximumOpaqueIDBytes)
	default:
		return false
	}
}

func (plan CleanupPlan) Validate() error {
	if plan.SchemaVersion != SchemaVersion || !validOpaqueValue(plan.ID, maximumOpaqueIDBytes) ||
		plan.Revision < 1 || !validCleanupScope(plan.Scope) || plan.Candidates == nil || plan.Protected == nil ||
		plan.CreatedAt.IsZero() || !plan.ExpiresAt.After(plan.CreatedAt) || len(plan.Candidates) > 10_000 || len(plan.Protected) > 10_000 {
		return fmt.Errorf("cleanup plan is invalid")
	}
	seen := make(map[string]struct{}, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		if !validOpaqueValue(candidate.ID, maximumOpaqueIDBytes) || candidate.Kind != "private-preparation" ||
			!validOpaqueValue(candidate.ProfileKey, maximumOpaqueIDBytes) || !validOpaqueValue(candidate.WorktreeID, maximumOpaqueIDBytes) ||
			!workspaceFingerprintPattern.MatchString(candidate.Fingerprint) || candidate.Bytes < 0 || candidate.Path == "" || len(candidate.Path) > maximumCleanupPathBytes {
			return fmt.Errorf("cleanup candidate is invalid")
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			return fmt.Errorf("cleanup candidate is duplicated")
		}
		seen[candidate.ID] = struct{}{}
	}
	for _, protected := range plan.Protected {
		if protected.Kind != "private-preparation" || protected.Path == "" || len(protected.Path) > maximumCleanupPathBytes ||
			(protected.Reason != "current" && protected.Reason != "unverified" && protected.Reason != "foreign-or-modified") {
			return fmt.Errorf("cleanup protection is invalid")
		}
	}
	return nil
}

func (result CleanupResult) Validate() error {
	if result.SchemaVersion != SchemaVersion || !validOpaqueValue(result.PlanID, maximumOpaqueIDBytes) ||
		result.PlanRevision < 1 || result.Removals == nil || len(result.Removals) > 1024 || result.CompletedAt.IsZero() {
		return fmt.Errorf("cleanup result is invalid")
	}
	seen := make(map[string]struct{}, len(result.Removals))
	for _, removal := range result.Removals {
		if !validOpaqueValue(removal.CandidateID, maximumOpaqueIDBytes) ||
			(removal.Removed && removal.Reason != "") || (!removal.Removed && removal.Reason != "not-in-plan" && removal.Reason != "changed-or-protected") {
			return fmt.Errorf("cleanup removal is invalid")
		}
		if _, duplicate := seen[removal.CandidateID]; duplicate {
			return fmt.Errorf("cleanup removal is duplicated")
		}
		seen[removal.CandidateID] = struct{}{}
	}
	return nil
}

func (action ProfileAction) Validate() error {
	if !validOpaqueValue(action.ID, maximumOpaqueIDBytes) || !validOpaqueValue(action.RepositoryID, maximumOpaqueIDBytes) ||
		!validOpaqueValue(action.ProfileKey, maximumOpaqueIDBytes) || !strings.HasPrefix(action.ProfileDigest, "sha256:") ||
		!validOpaqueValue(action.DisplayName, maximumDisplayTextBytes) {
		return fmt.Errorf("profile action identity is invalid")
	}
	switch action.Scope {
	case "machine", "repository", "worktree", "environment", "service":
	default:
		return fmt.Errorf("profile action scope is invalid")
	}
	switch action.Risk {
	case "local", "remote-read", "remote-write":
	default:
		return fmt.Errorf("profile action risk is invalid")
	}
	switch action.Kind {
	case "command":
		if action.Lifecycle != "" {
			return fmt.Errorf("profile command action must not name a lifecycle")
		}
	case "lifecycle":
		switch action.Lifecycle {
		case "prepare", "start", "stop", "cleanup":
		default:
			return fmt.Errorf("profile lifecycle action is invalid")
		}
	default:
		return fmt.Errorf("profile action kind is invalid")
	}
	if action.RequiresConfirmation != (action.Risk == "remote-write") {
		return fmt.Errorf("profile action confirmation does not match its risk")
	}
	return nil
}

func (list ProfileActionList) Validate() error {
	if list.SchemaVersion != SchemaVersion || list.Actions == nil || len(list.Actions) > 4096 ||
		(list.AcceptedDigest != "" && !strings.HasPrefix(list.AcceptedDigest, "sha256:")) {
		return fmt.Errorf("profile action list is invalid")
	}
	seen := make(map[string]struct{}, len(list.Actions))
	for _, action := range list.Actions {
		if err := action.Validate(); err != nil {
			return err
		}
		key := action.RepositoryID + "\x00" + action.ID
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("profile action is duplicated")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (request RunProfileActionRequest) Validate() error {
	if err := request.MutationRequest.Validate(); err != nil {
		return err
	}
	if !validOpaqueValue(request.RepositoryID, maximumOpaqueIDBytes) || !validOpaqueValue(request.ActionID, maximumOpaqueIDBytes) {
		return fmt.Errorf("profile action identity is invalid")
	}
	for _, optional := range []string{request.WorktreeID, request.EnvironmentID, request.ServiceID, request.ConfirmedActionID} {
		if optional != "" && !validOpaqueValue(optional, maximumOpaqueIDBytes) {
			return fmt.Errorf("profile action target is invalid")
		}
	}
	if request.WorktreeID != "" && request.EnvironmentID != "" {
		return fmt.Errorf("profile action target names both a worktree and an environment")
	}
	if request.ServiceID != "" && request.EnvironmentID == "" {
		return fmt.Errorf("profile action service target requires an environment")
	}
	if request.ConfirmedActionID != "" && request.ConfirmedActionID != request.ActionID {
		return fmt.Errorf("confirmed action id does not match action id")
	}
	if request.ExpectedEnvironmentRevision != nil && request.EnvironmentID == "" {
		return fmt.Errorf("expected environment revision requires an environment target")
	}
	return nil
}

func (receipt MutationReceipt) Validate() error {
	if receipt.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version: got %d, want %d", receipt.SchemaVersion, SchemaVersion)
	}
	if !validOpaqueValue(receipt.RequestID, maximumOpaqueIDBytes) ||
		!validOpaqueValue(receipt.OperationID, maximumOpaqueIDBytes) {
		return fmt.Errorf("mutation receipt identifiers are invalid")
	}
	if receipt.AcceptedAt.IsZero() {
		return fmt.Errorf("mutation receipt acceptance time is required")
	}
	if receipt.EnvironmentID != "" && !validOpaqueValue(receipt.EnvironmentID, maximumOpaqueIDBytes) {
		return fmt.Errorf("mutation receipt environment id is invalid")
	}
	if receipt.RunID != "" && !validOpaqueValue(receipt.RunID, maximumOpaqueIDBytes) {
		return fmt.Errorf("mutation receipt run id is invalid")
	}
	return nil
}

func validOpaqueValue(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

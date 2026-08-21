package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

type ConfigurationStore interface {
	StageConfiguration(context.Context, int64, string, configuration.Loaded) (state.ConfigurationCandidate, error)
	AcceptConfiguration(context.Context, int64, string) (state.ConfigurationRevision, error)
	ReadAcceptedConfiguration(context.Context) (state.ConfigurationRevision, error)
}

var (
	// ErrConfigurationDesiredChanged reports that the desired file no longer
	// matches the digest a mutation expected.
	ErrConfigurationDesiredChanged = configuration.ErrDesiredChanged
	// ErrConfigurationRepositoryReferenced reports that live resources still
	// reference a repository the owner asked to remove.
	ErrConfigurationRepositoryReferenced = errors.New("repository is still referenced by live resources")
	// ErrConfigurationRepositoryEnabled reports a removal of a repository whose
	// accepted or desired entry is still enabled.
	ErrConfigurationRepositoryEnabled = errors.New("repository must be disabled and accepted before removal")
)

// ConfigurationRejectedError carries the bounded reason the compiler refused a
// desired configuration so the owner can fix it. Reasons name keys, fields,
// and YAML structure; they never include file contents or secret values.
type ConfigurationRejectedError struct {
	Reason string
}

func (err ConfigurationRejectedError) Error() string { return err.Reason }

const maximumRejectionReasonBytes = 512

var (
	// rejectionQuotedValue matches the backtick-quoted scalar the YAML decoder
	// echoes in "cannot unmarshal !!str `value` into ..." errors.
	rejectionQuotedValue = regexp.MustCompile("`[^`]*`")
	rejectionUserPath    = regexp.MustCompile(`/Users/[^/\s]+`)
)

// rejected bounds a compiler or loader error into a reason that names keys,
// fields, lines, and YAML structure but never a scalar value from the file or
// an account path: the desired file may hold a pasted secret by mistake, and
// the reason is published to every client that reads configuration status.
func rejected(err error) error {
	reason := rejectionQuotedValue.ReplaceAllString(err.Error(), "`[value]`")
	reason = rejectionUserPath.ReplaceAllString(reason, "/Users/[redacted]")
	if len(reason) > maximumRejectionReasonBytes {
		reason = reason[:maximumRejectionReasonBytes]
	}
	return ConfigurationRejectedError{Reason: reason}
}

// ConfigurationService is the daemon's owner of the private desired file. It
// is the only writer (invariant 7): clients describe generic repository
// entries and compare-and-swap against both the accepted revision and the
// desired-file digest; the service edits, recompiles, writes atomically, and
// stages a candidate that still needs explicit acceptance.
type ConfigurationService struct {
	Store           ConfigurationStore
	Path            string
	CompilerVersion string
	Restart         func()
	// References answers whether published resources still reference a
	// repository key. Nil means the service cannot prove safety and refuses
	// removal of any repository that is currently published.
	References StatusSource

	mutex sync.Mutex
}

func (service *ConfigurationService) Status(ctx context.Context) (contractv2.ConfigurationStatus, error) {
	accepted, err := service.Store.ReadAcceptedConfiguration(ctx)
	status := contractv2.ConfigurationStatus{SchemaVersion: contractv2.SchemaVersion, State: "missing"}
	switch {
	case errors.Is(err, state.ErrConfigurationNotAccepted):
	case err != nil:
		return contractv2.ConfigurationStatus{}, err
	default:
		status.State = "accepted"
		status.AcceptedRevision = accepted.Revision
		status.AcceptedDigest = accepted.Digest
	}
	status.Desired = service.desiredView()
	return status, nil
}

func (service *ConfigurationService) desiredView() *contractv2.ConfigurationDesiredFile {
	if service.Path == "" {
		return nil
	}
	desired := configuration.ReadDesired(service.Path)
	view := &contractv2.ConfigurationDesiredFile{
		Present: desired.Present, SourceDigest: desired.SourceDigest,
		Repositories: []contractv2.ConfigurationRepositoryEntry{},
	}
	if desired.Problem != nil {
		view.Problem = rejected(desired.Problem).Error()
	}
	for _, entry := range desired.Entries() {
		view.Repositories = append(view.Repositories, contractv2.ConfigurationRepositoryEntry{
			Key: entry.Key, Enabled: entry.Enabled, DisplayName: entry.DisplayName, Root: entry.Root,
			Remote: entry.Remote, DefaultBase: entry.DefaultBase, ManagedWorktreesRoot: entry.ManagedWorktreesRoot,
		})
	}
	if view.Validate() != nil {
		// The file parsed but carries values the contract cannot publish; keep
		// the digest so CAS still works and report the entries as unpublishable.
		view.Repositories = []contractv2.ConfigurationRepositoryEntry{}
		view.Problem = "desired repository entries cannot be published through the contract"
	}
	return view
}

func (service *ConfigurationService) Validate(
	ctx context.Context,
	request contractv2.ConfigurationValidationRequest,
) (contractv2.ConfigurationStatus, error) {
	if request.Validate() != nil || service.Store == nil || service.Path == "" || service.CompilerVersion == "" {
		return contractv2.ConfigurationStatus{}, errors.New("configuration service is unavailable")
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	loaded, err := configuration.LoadFile(service.Path)
	if err != nil {
		return contractv2.ConfigurationStatus{}, rejected(err)
	}
	return service.stage(ctx, request.ExpectedRevision, loaded)
}

func (service *ConfigurationService) stage(
	ctx context.Context,
	expectedRevision int64,
	loaded configuration.Loaded,
) (contractv2.ConfigurationStatus, error) {
	candidate, err := service.Store.StageConfiguration(ctx, expectedRevision, service.CompilerVersion, loaded)
	if err != nil {
		return contractv2.ConfigurationStatus{}, err
	}
	status := contractv2.ConfigurationStatus{
		SchemaVersion: contractv2.SchemaVersion, State: "pending",
		AcceptedRevision: expectedRevision,
		Candidate:        candidateContract(candidate),
	}
	if accepted, readErr := service.Store.ReadAcceptedConfiguration(ctx); readErr == nil {
		status.AcceptedDigest = accepted.Digest
		if accepted.Digest == candidate.Digest {
			status.State = "accepted"
			status.Candidate = nil
		}
	}
	status.Desired = service.desiredView()
	return status, nil
}

// MutateRepository adds, updates, enables, disables, or removes one generic
// repository entry in the desired file. Nothing is written unless the
// accepted revision and desired digest both match, the edited document
// compiles, and (for removal) no accepted or live resource still depends on
// the repository. The result is a staged candidate awaiting acceptance.
func (service *ConfigurationService) MutateRepository(
	ctx context.Context,
	request contractv2.ConfigurationRepositoryMutationRequest,
) (contractv2.ConfigurationStatus, error) {
	if request.Validate() != nil || service.Store == nil || service.Path == "" || service.CompilerVersion == "" {
		return contractv2.ConfigurationStatus{}, errors.New("configuration service is unavailable")
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()

	accepted, err := service.Store.ReadAcceptedConfiguration(ctx)
	acceptedRevision := int64(0)
	switch {
	case errors.Is(err, state.ErrConfigurationNotAccepted):
	case err != nil:
		return contractv2.ConfigurationStatus{}, err
	default:
		acceptedRevision = accepted.Revision
	}
	if acceptedRevision != request.ExpectedRevision {
		return contractv2.ConfigurationStatus{}, state.ErrConfigurationRevisionConflict
	}

	desired := configuration.ReadDesired(service.Path)
	if desired.Present && desired.Problem != nil && desired.SourceDigest == "" {
		// The file exists but cannot even be read safely (symlink, hard link,
		// ownership, or mode). Never write over something we could not verify.
		return contractv2.ConfigurationStatus{}, rejected(desired.Problem)
	}
	if desired.SourceDigest != request.ExpectedSourceDigest {
		return contractv2.ConfigurationStatus{}, ErrConfigurationDesiredChanged
	}

	var edited []byte
	switch request.Operation {
	case contractv2.ConfigurationRepositoryUpsert:
		entry := configuration.RepositoryEntry{
			Key: request.Entry.Key, Enabled: request.Entry.Enabled, DisplayName: request.Entry.DisplayName,
			Root: request.Entry.Root, Remote: request.Entry.Remote, DefaultBase: request.Entry.DefaultBase,
			ManagedWorktreesRoot: request.Entry.ManagedWorktreesRoot,
		}
		if !desired.Present {
			edited, err = configuration.NewDocument(entry)
		} else {
			if !entry.Enabled {
				if err := service.disableAllowed(ctx, entry.Key, desired); err != nil {
					return contractv2.ConfigurationStatus{}, err
				}
			}
			edited, err = configuration.UpsertRepository(desired.Contents, entry)
		}
	case contractv2.ConfigurationRepositoryRemove:
		if !desired.Present {
			return contractv2.ConfigurationStatus{}, configuration.ErrRepositoryMissing
		}
		if err := service.removalAllowed(ctx, request.Key, desired, accepted); err != nil {
			return contractv2.ConfigurationStatus{}, err
		}
		edited, err = configuration.RemoveRepository(desired.Contents, request.Key)
	}
	if err != nil {
		if errors.Is(err, configuration.ErrRepositoryRootBound) || errors.Is(err, configuration.ErrRepositoryMissing) {
			return contractv2.ConfigurationStatus{}, err
		}
		return contractv2.ConfigurationStatus{}, rejected(err)
	}

	loaded, err := configuration.Parse(edited)
	if err != nil {
		return contractv2.ConfigurationStatus{}, rejected(err)
	}
	if err := configuration.WriteDesired(service.Path, edited, request.ExpectedSourceDigest); err != nil {
		if errors.Is(err, configuration.ErrDesiredChanged) {
			return contractv2.ConfigurationStatus{}, err
		}
		return contractv2.ConfigurationStatus{}, rejected(err)
	}
	return service.stage(ctx, request.ExpectedRevision, loaded)
}

// disableAllowed refuses to disable a repository that still owns a live
// environment. Once the disabled revision is accepted the daemon restarts
// without a registration for that repository, and a live environment without
// a registration fails boot closed; refusing here keeps the daemon bootable.
// A key the desired file does not know yet, or a malformed file, is left to
// the upsert and compiler to report.
func (service *ConfigurationService) disableAllowed(ctx context.Context, key string, desired configuration.Desired) error {
	if desired.Problem != nil {
		return nil
	}
	if repository, configured := desired.Document.Repositories[key]; !configured || !repository.Enabled {
		return nil
	}
	if service.References == nil {
		return ErrConfigurationRepositoryReferenced
	}
	snapshot, err := service.References.ReadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("read references: %w", err)
	}
	for _, published := range snapshot.Repositories {
		if published.ProfileKey != key {
			continue
		}
		for _, environment := range snapshot.Environments {
			if environment.RepositoryID == published.ID && environment.ObservedState != "stopped" {
				return ErrConfigurationRepositoryReferenced
			}
		}
	}
	return nil
}

// removalAllowed enforces the documented safe path: disable, accept, clean up
// or archive, then remove. The desired entry must be disabled, the accepted
// revision must no longer enable it, and no published environment or managed
// worktree may still belong to it.
func (service *ConfigurationService) removalAllowed(
	ctx context.Context,
	key string,
	desired configuration.Desired,
	accepted state.ConfigurationRevision,
) error {
	if desired.Problem != nil {
		return rejected(desired.Problem)
	}
	repository, configured := desired.Document.Repositories[key]
	if !configured {
		return configuration.ErrRepositoryMissing
	}
	if repository.Enabled {
		return ErrConfigurationRepositoryEnabled
	}
	if len(accepted.CanonicalPayload) > 0 {
		var document configuration.Document
		if err := json.Unmarshal(accepted.CanonicalPayload, &document); err != nil {
			return fmt.Errorf("decode accepted configuration: %w", err)
		}
		if acceptedRepository, published := document.Repositories[key]; published && acceptedRepository.Enabled {
			return ErrConfigurationRepositoryEnabled
		}
	}
	if service.References == nil {
		return ErrConfigurationRepositoryReferenced
	}
	snapshot, err := service.References.ReadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("read references: %w", err)
	}
	for _, published := range snapshot.Repositories {
		if published.ProfileKey != key {
			continue
		}
		for _, worktree := range published.Worktrees {
			if worktree.Workspace != nil && worktree.Workspace.Ownership == "managed" {
				return ErrConfigurationRepositoryReferenced
			}
		}
		for _, environment := range snapshot.Environments {
			if environment.RepositoryID == published.ID && environment.ObservedState != "stopped" {
				return ErrConfigurationRepositoryReferenced
			}
		}
	}
	return nil
}

func (service *ConfigurationService) Accept(
	ctx context.Context,
	request contractv2.ConfigurationAcceptanceRequest,
) (contractv2.ConfigurationStatus, error) {
	if request.Validate() != nil || service.Store == nil {
		return contractv2.ConfigurationStatus{}, errors.New("configuration service is unavailable")
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	accepted, err := service.Store.AcceptConfiguration(ctx, request.ExpectedRevision, request.Digest)
	if err != nil {
		return contractv2.ConfigurationStatus{}, err
	}
	status := contractv2.ConfigurationStatus{
		SchemaVersion: contractv2.SchemaVersion, State: "accepted",
		AcceptedRevision: accepted.Revision, AcceptedDigest: accepted.Digest,
		Desired: service.desiredView(),
	}
	if service.Restart != nil {
		time.AfterFunc(10*time.Millisecond, service.Restart)
	}
	return status, nil
}

func candidateContract(candidate state.ConfigurationCandidate) *contractv2.ConfigurationCandidate {
	return &contractv2.ConfigurationCandidate{
		SchemaVersion: contractv2.SchemaVersion, Digest: candidate.Digest,
		SourceDigest: candidate.SourceDigest, CompilerVersion: candidate.CompilerVersion,
		RepositoryDigests: candidate.RepositoryDigests, StagedAt: candidate.StagedAt,
		ExecutableDigests: candidate.ExecutableDigests,
	}
}

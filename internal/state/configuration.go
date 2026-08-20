package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
)

var (
	ErrConfigurationRevisionConflict = errors.New("configuration revision conflict")
	ErrConfigurationCandidateMissing = errors.New("configuration candidate is not staged")
	ErrConfigurationNotAccepted      = errors.New("configuration is not accepted")
)

type ConfigurationCandidate struct {
	Digest            string
	SchemaVersion     int
	SourceDigest      string
	CompilerVersion   string
	CanonicalPayload  json.RawMessage
	RepositoryDigests map[string]string
	ExecutableDigests map[string]string
	StagedAt          time.Time
}

type ConfigurationRevision struct {
	Revision int64
	ConfigurationCandidate
	AcceptedAt time.Time
}

func (store *Store) StageConfiguration(
	ctx context.Context,
	expectedRevision int64,
	compilerVersion string,
	loaded configuration.Loaded,
) (ConfigurationCandidate, error) {
	if compilerVersion == "" {
		return ConfigurationCandidate{}, errors.New("configuration compiler version is required")
	}
	if loaded.Digest == "" || loaded.SourceDigest == "" || len(loaded.CanonicalPayload) == 0 {
		return ConfigurationCandidate{}, errors.New("loaded configuration is incomplete")
	}
	repositoryDigests, err := json.Marshal(loaded.RepositoryDigests)
	if err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("encode repository digests: %w", err)
	}
	executableDigests, err := json.Marshal(loaded.ExecutableDigests)
	if err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("encode executable digests: %w", err)
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("begin configuration staging: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := requireConfigurationRevision(ctx, transaction, expectedRevision); err != nil {
		return ConfigurationCandidate{}, err
	}
	stagedAt := store.now().UTC()
	result, err := transaction.ExecContext(ctx, `
INSERT INTO configuration_candidates(
    digest, schema_version, source_digest, compiler_version, payload_json, repository_digests_json,
    executable_digests_json, staged_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(digest) DO NOTHING`,
		loaded.Digest, configuration.SchemaVersion, loaded.SourceDigest, compilerVersion,
		[]byte(loaded.CanonicalPayload), repositoryDigests, executableDigests, stagedAt.Format(timeFormat),
	)
	if err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("stage configuration: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("inspect configuration staging: %w", err)
	}
	if rowsAffected == 0 {
		existing, err := readConfigurationCandidate(ctx, transaction, loaded.Digest)
		if err != nil {
			return ConfigurationCandidate{}, err
		}
		if existing.SchemaVersion != configuration.SchemaVersion ||
			existing.SourceDigest != loaded.SourceDigest ||
			existing.CompilerVersion != compilerVersion ||
			!bytes.Equal(existing.CanonicalPayload, loaded.CanonicalPayload) ||
			!maps.Equal(existing.RepositoryDigests, loaded.RepositoryDigests) ||
			!maps.Equal(existing.ExecutableDigests, loaded.ExecutableDigests) {
			return ConfigurationCandidate{}, errors.New("staged configuration digest has different metadata")
		}
		stagedAt = existing.StagedAt
	}
	if err := pruneConfigurationCandidates(ctx, transaction); err != nil {
		return ConfigurationCandidate{}, err
	}
	if err := transaction.Commit(); err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("commit configuration staging: %w", err)
	}
	return ConfigurationCandidate{
		Digest: loaded.Digest, SchemaVersion: configuration.SchemaVersion,
		SourceDigest: loaded.SourceDigest, CompilerVersion: compilerVersion,
		CanonicalPayload:  append(json.RawMessage(nil), loaded.CanonicalPayload...),
		RepositoryDigests: cloneStringMap(loaded.RepositoryDigests),
		ExecutableDigests: cloneStringMap(loaded.ExecutableDigests), StagedAt: stagedAt,
	}, nil
}

func (store *Store) AcceptConfiguration(
	ctx context.Context,
	expectedRevision int64,
	digest string,
) (ConfigurationRevision, error) {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return ConfigurationRevision{}, fmt.Errorf("begin configuration acceptance: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := requireConfigurationRevision(ctx, transaction, expectedRevision); err != nil {
		return ConfigurationRevision{}, err
	}
	candidate, err := readConfigurationCandidate(ctx, transaction, digest)
	if err != nil {
		return ConfigurationRevision{}, err
	}
	acceptedAt := store.now().UTC()
	revision := expectedRevision + 1
	repositoryDigests, err := json.Marshal(candidate.RepositoryDigests)
	if err != nil {
		return ConfigurationRevision{}, fmt.Errorf("encode accepted repository digests: %w", err)
	}
	executableDigests, err := json.Marshal(candidate.ExecutableDigests)
	if err != nil {
		return ConfigurationRevision{}, fmt.Errorf("encode accepted executable digests: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO configuration_revisions(
    revision, digest, schema_version, source_digest, compiler_version, payload_json,
    repository_digests_json, executable_digests_json, accepted_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision, candidate.Digest, candidate.SchemaVersion, candidate.SourceDigest,
		candidate.CompilerVersion, []byte(candidate.CanonicalPayload), repositoryDigests, executableDigests,
		acceptedAt.Format(timeFormat),
	); err != nil {
		return ConfigurationRevision{}, fmt.Errorf("persist accepted configuration: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO configuration_head(singleton, revision) VALUES (1, ?)
ON CONFLICT(singleton) DO UPDATE SET revision = excluded.revision`, revision); err != nil {
		return ConfigurationRevision{}, fmt.Errorf("advance accepted configuration: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM configuration_candidates WHERE digest = ?", digest); err != nil {
		return ConfigurationRevision{}, fmt.Errorf("remove accepted configuration candidate: %w", err)
	}
	if err := pruneConfigurationRevisions(ctx, transaction); err != nil {
		return ConfigurationRevision{}, err
	}
	if err := transaction.Commit(); err != nil {
		return ConfigurationRevision{}, fmt.Errorf("commit configuration acceptance: %w", err)
	}
	return ConfigurationRevision{Revision: revision, ConfigurationCandidate: candidate, AcceptedAt: acceptedAt}, nil
}

func (store *Store) ReadAcceptedConfiguration(ctx context.Context) (ConfigurationRevision, error) {
	var revision ConfigurationRevision
	var payload, repositoryDigests, executableDigests []byte
	var acceptedAt string
	err := store.database.QueryRowContext(ctx, `
SELECT r.revision, r.digest, r.schema_version, r.source_digest, r.compiler_version,
       r.payload_json, r.repository_digests_json, r.executable_digests_json, r.accepted_at
FROM configuration_head AS h
JOIN configuration_revisions AS r ON r.revision = h.revision
WHERE h.singleton = 1`).Scan(
		&revision.Revision, &revision.Digest, &revision.SchemaVersion, &revision.SourceDigest,
		&revision.CompilerVersion, &payload, &repositoryDigests, &executableDigests, &acceptedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigurationRevision{}, ErrConfigurationNotAccepted
	}
	if err != nil {
		return ConfigurationRevision{}, fmt.Errorf("read accepted configuration: %w", err)
	}
	parsedTime, err := time.Parse(timeFormat, acceptedAt)
	if err != nil {
		return ConfigurationRevision{}, fmt.Errorf("parse accepted configuration time: %w", err)
	}
	if err := json.Unmarshal(repositoryDigests, &revision.RepositoryDigests); err != nil {
		return ConfigurationRevision{}, fmt.Errorf("decode accepted repository digests: %w", err)
	}
	if err := json.Unmarshal(executableDigests, &revision.ExecutableDigests); err != nil {
		return ConfigurationRevision{}, fmt.Errorf("decode accepted executable digests: %w", err)
	}
	revision.CanonicalPayload = append(json.RawMessage(nil), payload...)
	revision.AcceptedAt = parsedTime
	return revision, nil
}

func requireConfigurationRevision(ctx context.Context, transaction *sql.Tx, expected int64) error {
	var current int64
	err := transaction.QueryRowContext(ctx, "SELECT revision FROM configuration_head WHERE singleton = 1").Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = 0
	} else if err != nil {
		return fmt.Errorf("read configuration head: %w", err)
	}
	if current != expected {
		return fmt.Errorf("%w: expected %d, current %d", ErrConfigurationRevisionConflict, expected, current)
	}
	return nil
}

func readConfigurationCandidate(ctx context.Context, transaction *sql.Tx, digest string) (ConfigurationCandidate, error) {
	var candidate ConfigurationCandidate
	var payload, repositoryDigests, executableDigests []byte
	var stagedAt string
	err := transaction.QueryRowContext(ctx, `
SELECT digest, schema_version, source_digest, compiler_version, payload_json,
       repository_digests_json, executable_digests_json, staged_at
FROM configuration_candidates
WHERE digest = ?`, digest).Scan(
		&candidate.Digest, &candidate.SchemaVersion, &candidate.SourceDigest, &candidate.CompilerVersion,
		&payload, &repositoryDigests, &executableDigests, &stagedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigurationCandidate{}, ErrConfigurationCandidateMissing
	}
	if err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("read configuration candidate: %w", err)
	}
	parsedTime, err := time.Parse(timeFormat, stagedAt)
	if err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("parse configuration candidate time: %w", err)
	}
	if err := json.Unmarshal(repositoryDigests, &candidate.RepositoryDigests); err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("decode repository digests: %w", err)
	}
	if err := json.Unmarshal(executableDigests, &candidate.ExecutableDigests); err != nil {
		return ConfigurationCandidate{}, fmt.Errorf("decode executable digests: %w", err)
	}
	candidate.CanonicalPayload = append(json.RawMessage(nil), payload...)
	candidate.StagedAt = parsedTime
	return candidate, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

var ErrConfigurationRevisionMissing = errors.New("pinned configuration revision is not retained")

// PinnedRepositoryProfile returns the exact repository profile whose accepted
// per-repository digest matches. It searches every retained accepted revision,
// newest first, so a run pinned to an older revision recovers its payload after
// later acceptances. A digest without a retained payload is an error; callers
// must not fall back to the head.
func (store *Store) PinnedRepositoryProfile(
	ctx context.Context,
	profileKey string,
	repositoryDigest string,
) (configuration.Repository, error) {
	if profileKey == "" || repositoryDigest == "" {
		return configuration.Repository{}, errors.New("pinned profile key and digest are required")
	}
	rows, err := store.database.QueryContext(ctx, `
SELECT revision, payload_json, repository_digests_json
FROM configuration_revisions
ORDER BY revision DESC`)
	if err != nil {
		return configuration.Repository{}, fmt.Errorf("read accepted configuration revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var revision int64
		var payload, repositoryDigests []byte
		if err := rows.Scan(&revision, &payload, &repositoryDigests); err != nil {
			return configuration.Repository{}, fmt.Errorf("scan accepted configuration revision: %w", err)
		}
		var digests map[string]string
		if err := json.Unmarshal(repositoryDigests, &digests); err != nil {
			return configuration.Repository{}, fmt.Errorf("decode repository digests for revision %d: %w", revision, err)
		}
		if digests[profileKey] != repositoryDigest {
			continue
		}
		var document configuration.Document
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return configuration.Repository{}, fmt.Errorf("decode accepted configuration revision %d: %w", revision, err)
		}
		repository, found := document.Repositories[profileKey]
		if !found {
			return configuration.Repository{}, fmt.Errorf("accepted configuration revision %d does not contain %q", revision, profileKey)
		}
		return repository, nil
	}
	if err := rows.Err(); err != nil {
		return configuration.Repository{}, fmt.Errorf("iterate accepted configuration revisions: %w", err)
	}
	return configuration.Repository{}, ErrConfigurationRevisionMissing
}

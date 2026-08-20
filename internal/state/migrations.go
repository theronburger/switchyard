package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrUnsupportedSchemaVersion = errors.New("state database schema is newer than this daemon")

type migration struct {
	version int
	sql     string
	// apply runs after sql inside the same transaction for data migrations
	// that cannot be expressed as schema statements.
	apply func(context.Context, *sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE snapshot_revisions (
    revision INTEGER PRIMARY KEY,
    payload_json BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE snapshot_head (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL REFERENCES snapshot_revisions(revision)
);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    request_fingerprint BLOB NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    environment_id TEXT,
    environment_revision INTEGER,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    error_json BLOB
);

CREATE INDEX operations_environment_updated
    ON operations(environment_id, updated_at DESC);

CREATE TABLE events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    snapshot_revision INTEGER NOT NULL,
    kind TEXT NOT NULL,
    environment_id TEXT,
    occurred_at TEXT NOT NULL,
    payload_json BLOB NOT NULL
);

CREATE INDEX events_environment_sequence
    ON events(environment_id, sequence);
`,
	},
	{
		version: 2,
		sql: `
CREATE TABLE environment_operation_records (
    operation_id TEXT PRIMARY KEY REFERENCES operations(id),
    schema_version INTEGER NOT NULL,
    environment_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    operation_state TEXT NOT NULL,
    record_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX environment_operation_records_incomplete
    ON environment_operation_records(operation_state, created_at, operation_id);

CREATE TABLE environment_current_results (
    environment_id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL,
    operation_id TEXT NOT NULL REFERENCES environment_operation_records(operation_id),
    result_json BLOB NOT NULL,
    updated_at TEXT NOT NULL
);
`,
	},
	{
		version: 3,
		sql: `
CREATE TABLE workspace_operation_records (
    operation_id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL,
    worktree_id TEXT NOT NULL,
    workspace_state TEXT NOT NULL,
    record_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX workspace_operation_records_incomplete_worktree
    ON workspace_operation_records(worktree_id)
    WHERE workspace_state IN ('pending', 'running');

CREATE INDEX workspace_operation_records_incomplete
    ON workspace_operation_records(workspace_state, created_at, operation_id);

CREATE TABLE workspace_current_results (
    worktree_id TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL,
    operation_id TEXT NOT NULL REFERENCES workspace_operation_records(operation_id),
    result_json BLOB NOT NULL,
    updated_at TEXT NOT NULL
);
`,
	},
	{
		version: 4,
		sql: `
ALTER TABLE operations ADD COLUMN phase TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 5,
		sql: `
ALTER TABLE operations ADD COLUMN run_id TEXT;
`,
	},
	{
		version: 6,
		sql: `
CREATE TABLE current_snapshot (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL,
    payload_json BLOB NOT NULL,
    created_at TEXT NOT NULL
);

INSERT INTO current_snapshot(singleton, revision, payload_json, created_at)
SELECT 1, snapshot_revisions.revision, snapshot_revisions.payload_json, snapshot_revisions.created_at
FROM snapshot_head
JOIN snapshot_revisions ON snapshot_revisions.revision = snapshot_head.revision
WHERE snapshot_head.singleton = 1;

DROP TABLE snapshot_head;
DROP TABLE snapshot_revisions;
`,
	},
	{
		version: 7,
		sql: `
CREATE TABLE configuration_candidates (
    digest TEXT PRIMARY KEY,
    schema_version INTEGER NOT NULL,
    source_digest TEXT NOT NULL,
    compiler_version TEXT NOT NULL,
    payload_json BLOB NOT NULL,
    repository_digests_json BLOB NOT NULL,
    staged_at TEXT NOT NULL
);

CREATE TABLE configuration_revisions (
    revision INTEGER PRIMARY KEY,
    digest TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    source_digest TEXT NOT NULL,
    compiler_version TEXT NOT NULL,
    payload_json BLOB NOT NULL,
    repository_digests_json BLOB NOT NULL,
    accepted_at TEXT NOT NULL,
    UNIQUE(digest)
);

CREATE TABLE configuration_head (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL REFERENCES configuration_revisions(revision)
);
`,
	},
	{
		version: 8,
		sql: `
CREATE TABLE cleanup_plans (
    revision INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    schema_version INTEGER NOT NULL,
    plan_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    consumed_at TEXT
);

CREATE INDEX cleanup_plans_expiration
    ON cleanup_plans(expires_at, consumed_at);
`,
	},
	{
		version: 9,
		sql: `
ALTER TABLE configuration_candidates
    ADD COLUMN executable_digests_json BLOB NOT NULL DEFAULT '{}';

ALTER TABLE configuration_revisions
    ADD COLUMN executable_digests_json BLOB NOT NULL DEFAULT '{}';
`,
	},
	{
		// Contract v2 and record schema 2: the public repository field
		// `adapter` becomes `profileKey`, pinned environment intent carries
		// `ProfileDigest` instead of `Adapter`, and workspace results carry
		// `ProfileKey`. Existing 0.1.0 state is rewritten in place so strict
		// decoders never see the legacy shape.
		version: 10,
		apply:   migrateLegacyProfileNaming,
	},
	{
		// Cleanup apply is a claimed transaction: authorization for one plan
		// revision is recorded atomically before any owned resource is
		// mutated, every candidate outcome is journaled as it becomes final,
		// and an interrupted apply survives restarts as an incomplete claim.
		version: 11,
		sql: `
CREATE TABLE IF NOT EXISTS cleanup_applies (
    plan_revision INTEGER PRIMARY KEY REFERENCES cleanup_plans(revision) ON DELETE CASCADE,
    plan_id TEXT NOT NULL UNIQUE,
    claim_json BLOB NOT NULL,
    claimed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);
`,
	},
	{
		// Explicit worktree occupancy leases: an owner-launched handoff is
		// recorded only when a client acquires a lease and ends only when a
		// client releases it. Held leases protect a worktree from archive.
		version: 12,
		sql: `
CREATE TABLE occupancy_leases (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    worktree_id TEXT NOT NULL,
    holder_kind TEXT NOT NULL,
    holder_label TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('held', 'released')),
    acquired_at TEXT NOT NULL,
    released_at TEXT
);

CREATE INDEX occupancy_leases_worktree_state
    ON occupancy_leases(worktree_id, state);
`,
	},
}

func (store *Store) migrate(ctx context.Context) error {
	if _, err := store.database.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	var currentVersion int
	if err := store.database.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	latestVersion := migrations[len(migrations)-1].version
	if currentVersion > latestVersion {
		return fmt.Errorf("%w: database is %d, daemon supports %d", ErrUnsupportedSchemaVersion, currentVersion, latestVersion)
	}

	for _, nextMigration := range migrations {
		if nextMigration.version <= currentVersion {
			continue
		}
		if err := store.applyMigration(ctx, nextMigration); err != nil {
			return err
		}
		currentVersion = nextMigration.version
	}
	return nil
}

func (store *Store) applyMigration(ctx context.Context, nextMigration migration) error {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", nextMigration.version, err)
	}

	if nextMigration.sql != "" {
		if _, err := transaction.ExecContext(ctx, nextMigration.sql); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %d: %w", nextMigration.version, err)
		}
	}
	if nextMigration.apply != nil {
		if err := nextMigration.apply(ctx, transaction); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %d: %w", nextMigration.version, err)
		}
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
		nextMigration.version,
		store.now().UTC().Format(timeFormat),
	); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("record migration %d: %w", nextMigration.version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", nextMigration.version, err)
	}
	return nil
}

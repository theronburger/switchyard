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

	if _, err := transaction.ExecContext(ctx, nextMigration.sql); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("apply migration %d: %w", nextMigration.version, err)
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

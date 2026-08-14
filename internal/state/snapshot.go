package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const timeFormat = time.RFC3339Nano

func (store *Store) CommitSnapshot(ctx context.Context, snapshot contractv1.StatusSnapshot) (contractv1.StatusSnapshot, error) {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("begin status snapshot: %w", err)
	}
	committed, err := store.commitSnapshotTransaction(ctx, transaction, snapshot)
	if err != nil {
		_ = transaction.Rollback()
		return contractv1.StatusSnapshot{}, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("commit status snapshot: %w", err)
	}
	return committed, nil
}

func (store *Store) commitSnapshotTransaction(
	ctx context.Context,
	transaction *sql.Tx,
	snapshot contractv1.StatusSnapshot,
) (contractv1.StatusSnapshot, error) {
	var currentRevision int64
	err := transaction.QueryRowContext(ctx, "SELECT revision FROM snapshot_head WHERE singleton = 1").Scan(&currentRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return contractv1.StatusSnapshot{}, fmt.Errorf("read status revision: %w", err)
	}

	snapshot.SchemaVersion = contractv1.SchemaVersion
	snapshot.SnapshotRevision = currentRevision + 1
	snapshot.GeneratedAt = store.now().UTC()
	if err := snapshot.Validate(); err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("validate status snapshot: %w", err)
	}

	payload, err := json.Marshal(snapshot)
	if err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("encode status snapshot: %w", err)
	}

	if _, err := transaction.ExecContext(
		ctx,
		"INSERT INTO snapshot_revisions(revision, payload_json, created_at) VALUES (?, ?, ?)",
		snapshot.SnapshotRevision,
		payload,
		snapshot.GeneratedAt.Format(timeFormat),
	); err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("persist status snapshot: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO snapshot_head(singleton, revision) VALUES (1, ?)
ON CONFLICT(singleton) DO UPDATE SET revision = excluded.revision`, snapshot.SnapshotRevision); err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("advance status snapshot: %w", err)
	}
	return snapshot, nil
}

func (store *Store) ReadSnapshot(ctx context.Context) (contractv1.StatusSnapshot, error) {
	var revision int64
	var payload []byte
	err := store.database.QueryRowContext(ctx, `
SELECT snapshot_head.revision, snapshot_revisions.payload_json
FROM snapshot_head
JOIN snapshot_revisions ON snapshot_revisions.revision = snapshot_head.revision
WHERE snapshot_head.singleton = 1`).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return contractv1.StatusSnapshot{}, ErrNoSnapshot
	}
	if err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("read status snapshot: %w", err)
	}

	var snapshot contractv1.StatusSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("decode status snapshot: %w", err)
	}
	if snapshot.SnapshotRevision != revision {
		return contractv1.StatusSnapshot{}, fmt.Errorf("status snapshot revision mismatch: head %d, payload %d", revision, snapshot.SnapshotRevision)
	}
	if err := snapshot.Validate(); err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("validate stored status snapshot: %w", err)
	}
	return snapshot, nil
}

func (store *Store) commitOperationsSnapshot(ctx context.Context, transaction *sql.Tx) error {
	var revision int64
	var payload []byte
	err := transaction.QueryRowContext(ctx, `
SELECT snapshot_head.revision, snapshot_revisions.payload_json
FROM snapshot_head
JOIN snapshot_revisions ON snapshot_revisions.revision = snapshot_head.revision
WHERE snapshot_head.singleton = 1`).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read status snapshot for operation: %w", err)
	}

	var snapshot contractv1.StatusSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return fmt.Errorf("decode status snapshot for operation: %w", err)
	}
	if snapshot.SnapshotRevision != revision {
		return fmt.Errorf("status snapshot revision mismatch: head %d, payload %d", revision, snapshot.SnapshotRevision)
	}
	operations, err := listOperations(ctx, transaction)
	if err != nil {
		return fmt.Errorf("list operations for status snapshot: %w", err)
	}
	snapshot.Operations = operations
	if _, err := store.commitSnapshotTransaction(ctx, transaction, snapshot); err != nil {
		return fmt.Errorf("commit operation status snapshot: %w", err)
	}
	return nil
}

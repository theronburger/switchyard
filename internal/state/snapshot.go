package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

const timeFormat = time.RFC3339Nano

type SnapshotUpdater func(*contractv2.StatusSnapshot) (bool, error)

func (store *Store) CommitSnapshot(ctx context.Context, snapshot contractv2.StatusSnapshot) (contractv2.StatusSnapshot, error) {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("begin status snapshot: %w", err)
	}
	committed, err := store.commitSnapshotTransaction(ctx, transaction, snapshot)
	if err != nil {
		_ = transaction.Rollback()
		return contractv2.StatusSnapshot{}, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("commit status snapshot: %w", err)
	}
	return committed, nil
}

// UpdateSnapshot applies a fast, side-effect-free mutation to the latest
// snapshot inside the store transaction. External observation must happen
// before this call so a stale read cannot overwrite a concurrent environment
// or operation publication.
func (store *Store) UpdateSnapshot(
	ctx context.Context,
	update SnapshotUpdater,
) (contractv2.StatusSnapshot, bool, error) {
	if update == nil {
		return contractv2.StatusSnapshot{}, false, errors.New("snapshot updater is required")
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv2.StatusSnapshot{}, false, fmt.Errorf("begin status snapshot update: %w", err)
	}
	snapshot, err := readSnapshotTransaction(ctx, transaction)
	if err != nil {
		_ = transaction.Rollback()
		return contractv2.StatusSnapshot{}, false, err
	}
	changed, err := update(&snapshot)
	if err != nil {
		_ = transaction.Rollback()
		return contractv2.StatusSnapshot{}, false, err
	}
	if !changed {
		if err := transaction.Rollback(); err != nil {
			return contractv2.StatusSnapshot{}, false, fmt.Errorf("close unchanged status snapshot: %w", err)
		}
		return snapshot, false, nil
	}
	committed, err := store.commitSnapshotTransaction(ctx, transaction, snapshot)
	if err != nil {
		_ = transaction.Rollback()
		return contractv2.StatusSnapshot{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv2.StatusSnapshot{}, false, fmt.Errorf("commit status snapshot update: %w", err)
	}
	return committed, true, nil
}

func (store *Store) commitSnapshotTransaction(
	ctx context.Context,
	transaction *sql.Tx,
	snapshot contractv2.StatusSnapshot,
) (contractv2.StatusSnapshot, error) {
	var currentRevision int64
	err := transaction.QueryRowContext(ctx, "SELECT revision FROM current_snapshot WHERE singleton = 1").Scan(&currentRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return contractv2.StatusSnapshot{}, fmt.Errorf("read status revision: %w", err)
	}

	snapshot.SchemaVersion = contractv2.SchemaVersion
	snapshot.SnapshotRevision = currentRevision + 1
	snapshot.GeneratedAt = store.now().UTC()
	if err := snapshot.Validate(); err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("validate status snapshot: %w", err)
	}

	payload, err := json.Marshal(snapshot)
	if err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("encode status snapshot: %w", err)
	}

	if _, err := transaction.ExecContext(ctx, `
INSERT INTO current_snapshot(singleton, revision, payload_json, created_at) VALUES (1, ?, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
    revision = excluded.revision,
    payload_json = excluded.payload_json,
    created_at = excluded.created_at`,
		snapshot.SnapshotRevision,
		payload,
		snapshot.GeneratedAt.Format(timeFormat),
	); err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("advance status snapshot: %w", err)
	}
	return snapshot, nil
}

func (store *Store) ReadSnapshot(ctx context.Context) (contractv2.StatusSnapshot, error) {
	var revision int64
	var payload []byte
	err := store.database.QueryRowContext(ctx, `
SELECT revision, payload_json
FROM current_snapshot
WHERE singleton = 1`).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return contractv2.StatusSnapshot{}, ErrNoSnapshot
	}
	if err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("read status snapshot: %w", err)
	}

	var snapshot contractv2.StatusSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("decode status snapshot: %w", err)
	}
	if snapshot.SnapshotRevision != revision {
		return contractv2.StatusSnapshot{}, fmt.Errorf("status snapshot revision mismatch: head %d, payload %d", revision, snapshot.SnapshotRevision)
	}
	if err := snapshot.Validate(); err != nil {
		return contractv2.StatusSnapshot{}, fmt.Errorf("validate stored status snapshot: %w", err)
	}
	return snapshot, nil
}

func (store *Store) commitOperationsSnapshot(ctx context.Context, transaction *sql.Tx) error {
	var revision int64
	var payload []byte
	err := transaction.QueryRowContext(ctx, `
SELECT revision, payload_json
FROM current_snapshot
WHERE singleton = 1`).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read status snapshot for operation: %w", err)
	}

	var snapshot contractv2.StatusSnapshot
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

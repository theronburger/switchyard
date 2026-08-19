package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
)

const workspaceRecordSchemaVersion = 1

var (
	ErrWorkspaceRecordExists   = errors.New("workspace operation record already exists")
	ErrWorkspaceRecordNotFound = errors.New("workspace operation record not found")
	ErrWorkspaceRecordInvalid  = errors.New("workspace operation record is invalid")
	ErrWorkspaceResultInvalid  = errors.New("workspace result is invalid")
	ErrWorkspaceRecordVersion  = errors.New("workspace record schema version is unsupported")
)

type WorkspaceJournal struct {
	store *Store
}

func NewWorkspaceJournal(store *Store) (*WorkspaceJournal, error) {
	if store == nil {
		return nil, ErrWorkspaceRecordInvalid
	}
	return &WorkspaceJournal{store: store}, nil
}

func (journal *WorkspaceJournal) Begin(ctx context.Context, record workspacecontrol.OperationRecord) error {
	if record.Validate() != nil || record.State != workspacecontrol.StatePending ||
		record.Phase != workspacecontrol.PhasePending {
		return ErrWorkspaceRecordInvalid
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return ErrWorkspaceRecordInvalid
	}
	transaction, err := journal.store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin workspace operation creation: %w", err)
	}
	defer transaction.Rollback()
	var existing int
	if err := transaction.QueryRowContext(ctx, `
SELECT COUNT(*) FROM workspace_operation_records
WHERE operation_id = ? OR (worktree_id = ? AND workspace_state IN ('pending', 'running'))`,
		record.OperationID, record.WorktreeID).Scan(&existing); err != nil {
		return fmt.Errorf("check existing workspace operation: %w", err)
	}
	if existing != 0 {
		return ErrWorkspaceRecordExists
	}
	now := journal.store.now().UTC().Format(timeFormat)
	_, err = transaction.ExecContext(ctx, `
INSERT INTO workspace_operation_records(
    operation_id, schema_version, worktree_id, workspace_state, record_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`, record.OperationID, workspaceRecordSchemaVersion,
		record.WorktreeID, string(record.State), payload, now, now)
	if err != nil {
		return fmt.Errorf("persist workspace operation: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit workspace operation creation: %w", err)
	}
	return nil
}

func (journal *WorkspaceJournal) Update(ctx context.Context, record workspacecontrol.OperationRecord) error {
	if record.Validate() != nil {
		return ErrWorkspaceRecordInvalid
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return ErrWorkspaceRecordInvalid
	}
	transaction, err := journal.store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin workspace operation update: %w", err)
	}
	defer transaction.Rollback()
	current, err := readWorkspaceRecord(ctx, transaction, record.OperationID, record.WorktreeID)
	if err != nil {
		return err
	}
	if !validWorkspaceTransition(current, record) {
		return ErrWorkspaceRecordInvalid
	}
	result, err := transaction.ExecContext(ctx, `
UPDATE workspace_operation_records
SET workspace_state = ?, record_json = ?, updated_at = ?
WHERE operation_id = ? AND worktree_id = ?`, string(record.State), payload,
		journal.store.now().UTC().Format(timeFormat), record.OperationID, record.WorktreeID)
	if err != nil {
		return fmt.Errorf("persist workspace operation update: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect workspace operation update: %w", err)
	}
	if updated != 1 {
		return ErrWorkspaceRecordNotFound
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit workspace operation update: %w", err)
	}
	return nil
}

func (journal *WorkspaceJournal) Publish(
	ctx context.Context,
	record workspacecontrol.OperationRecord,
	result workspacecontrol.Result,
) error {
	if record.Validate() != nil || result.Validate() != nil || record.State != workspacecontrol.StateReady ||
		record.Phase != workspacecontrol.PhaseComplete || result.State != workspacecontrol.StateReady ||
		record.WorktreeID != result.WorktreeID || record.Fingerprint != result.Fingerprint {
		return ErrWorkspaceResultInvalid
	}
	recordPayload, err := json.Marshal(record)
	if err != nil {
		return ErrWorkspaceRecordInvalid
	}
	resultPayload, err := json.Marshal(result)
	if err != nil {
		return ErrWorkspaceResultInvalid
	}
	transaction, err := journal.store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin workspace publication: %w", err)
	}
	defer transaction.Rollback()
	currentRecord, err := readWorkspaceRecord(ctx, transaction, record.OperationID, record.WorktreeID)
	if err != nil {
		return err
	}
	if currentRecord.State != workspacecontrol.StateRunning || record.State != workspacecontrol.StateReady ||
		record.NextStep != record.StepCount || record.NextStep < currentRecord.NextStep {
		return ErrWorkspaceRecordInvalid
	}
	now := journal.store.now().UTC().Format(timeFormat)
	update, err := transaction.ExecContext(ctx, `
UPDATE workspace_operation_records
SET workspace_state = ?, record_json = ?, updated_at = ?
WHERE operation_id = ? AND worktree_id = ?`, string(record.State), recordPayload, now,
		record.OperationID, record.WorktreeID)
	if err != nil {
		return fmt.Errorf("persist terminal workspace operation: %w", err)
	}
	updated, err := update.RowsAffected()
	if err != nil || updated != 1 {
		return ErrWorkspaceRecordNotFound
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO workspace_current_results(worktree_id, schema_version, operation_id, result_json, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(worktree_id) DO UPDATE SET
    schema_version = excluded.schema_version,
    operation_id = excluded.operation_id,
    result_json = excluded.result_json,
    updated_at = excluded.updated_at`, result.WorktreeID, workspaceRecordSchemaVersion,
		record.OperationID, resultPayload, now); err != nil {
		return fmt.Errorf("persist current workspace result: %w", err)
	}
	snapshot, snapshotErr := readSnapshotTransaction(ctx, transaction)
	if snapshotErr == nil {
		if !projectWorkspaceResult(&snapshot, result) || snapshot.Validate() != nil {
			return ErrWorkspaceResultInvalid
		}
		if _, err := journal.store.commitSnapshotTransaction(ctx, transaction, snapshot); err != nil {
			return fmt.Errorf("publish workspace status snapshot: %w", err)
		}
	} else if !errors.Is(snapshotErr, ErrNoSnapshot) {
		return snapshotErr
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit workspace publication: %w", err)
	}
	return nil
}

func projectWorkspaceResult(snapshot *contractv1.StatusSnapshot, result workspacecontrol.Result) bool {
	for repositoryIndex := range snapshot.Repositories {
		for worktreeIndex := range snapshot.Repositories[repositoryIndex].Worktrees {
			worktree := &snapshot.Repositories[repositoryIndex].Worktrees[worktreeIndex]
			if worktree.ID != result.WorktreeID {
				continue
			}
			toolchains := make([]contractv1.WorkspaceToolchain, len(result.Toolchains))
			for index, toolchain := range result.Toolchains {
				toolchains[index] = contractv1.WorkspaceToolchain{
					ID: toolchain.ID, RequestedVersion: toolchain.RequestedVersion,
					ResolvedVersion: toolchain.ResolvedVersion,
				}
			}
			sort.Slice(toolchains, func(left, right int) bool { return toolchains[left].ID < toolchains[right].ID })
			worktree.Workspace = &contractv1.WorkspaceStatus{
				Ownership: string(result.Ownership), State: string(result.State),
				Fingerprint: result.Fingerprint, PreparedAt: result.PreparedAt, Toolchains: toolchains,
			}
			return true
		}
	}
	return false
}

func (journal *WorkspaceJournal) Current(
	ctx context.Context,
	worktreeID string,
) (workspacecontrol.Result, bool, error) {
	var version int
	var payload []byte
	err := journal.store.database.QueryRowContext(ctx, `
SELECT schema_version, result_json
FROM workspace_current_results
WHERE worktree_id = ?`, worktreeID).Scan(&version, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return workspacecontrol.Result{}, false, nil
	}
	if err != nil {
		return workspacecontrol.Result{}, false, fmt.Errorf("read current workspace result: %w", err)
	}
	if version != workspaceRecordSchemaVersion {
		return workspacecontrol.Result{}, false, ErrWorkspaceRecordVersion
	}
	var result workspacecontrol.Result
	if err := decodeStrictWorkspaceJSON(payload, &result); err != nil || result.WorktreeID != worktreeID {
		return workspacecontrol.Result{}, false, ErrWorkspaceResultInvalid
	}
	if result.Validate() != nil {
		return workspacecontrol.Result{}, false, ErrWorkspaceResultInvalid
	}
	return result, true, nil
}

func (journal *WorkspaceJournal) Incomplete(ctx context.Context) ([]workspacecontrol.OperationRecord, error) {
	rows, err := journal.store.database.QueryContext(ctx, `
SELECT schema_version, record_json
FROM workspace_operation_records
WHERE workspace_state IN ('pending', 'running')
ORDER BY created_at, operation_id`)
	if err != nil {
		return nil, fmt.Errorf("list incomplete workspace operations: %w", err)
	}
	defer rows.Close()
	records := make([]workspacecontrol.OperationRecord, 0)
	for rows.Next() {
		var version int
		var payload []byte
		if err := rows.Scan(&version, &payload); err != nil {
			return nil, fmt.Errorf("scan incomplete workspace operation: %w", err)
		}
		if version != workspaceRecordSchemaVersion {
			return nil, ErrWorkspaceRecordVersion
		}
		var record workspacecontrol.OperationRecord
		if err := decodeStrictWorkspaceJSON(payload, &record); err != nil {
			return nil, ErrWorkspaceRecordInvalid
		}
		if record.Validate() != nil {
			return nil, ErrWorkspaceRecordInvalid
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incomplete workspace operations: %w", err)
	}
	sort.Slice(records, func(left, right int) bool {
		return records[left].OperationID < records[right].OperationID
	})
	return records, nil
}

func (journal *WorkspaceJournal) ListCurrent(ctx context.Context) ([]workspacecontrol.Result, error) {
	rows, err := journal.store.database.QueryContext(ctx, `
SELECT worktree_id, schema_version, result_json
FROM workspace_current_results
ORDER BY worktree_id`)
	if err != nil {
		return nil, fmt.Errorf("list current workspace results: %w", err)
	}
	defer rows.Close()
	results := make([]workspacecontrol.Result, 0)
	for rows.Next() {
		var worktreeID string
		var version int
		var payload []byte
		if err := rows.Scan(&worktreeID, &version, &payload); err != nil {
			return nil, fmt.Errorf("scan current workspace result: %w", err)
		}
		if version != workspaceRecordSchemaVersion {
			return nil, ErrWorkspaceRecordVersion
		}
		var result workspacecontrol.Result
		if err := decodeStrictWorkspaceJSON(payload, &result); err != nil ||
			result.Validate() != nil || result.WorktreeID != worktreeID {
			return nil, ErrWorkspaceResultInvalid
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current workspace results: %w", err)
	}
	return results, nil
}

func decodeStrictWorkspaceJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrWorkspaceRecordInvalid
	}
	return nil
}

type workspaceRecordReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readWorkspaceRecord(
	ctx context.Context,
	reader workspaceRecordReader,
	operationID string,
	worktreeID string,
) (workspacecontrol.OperationRecord, error) {
	var version int
	var payload []byte
	err := reader.QueryRowContext(ctx, `
SELECT schema_version, record_json
FROM workspace_operation_records
WHERE operation_id = ? AND worktree_id = ?`, operationID, worktreeID).Scan(&version, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return workspacecontrol.OperationRecord{}, ErrWorkspaceRecordNotFound
	}
	if err != nil {
		return workspacecontrol.OperationRecord{}, fmt.Errorf("read workspace operation: %w", err)
	}
	if version != workspaceRecordSchemaVersion {
		return workspacecontrol.OperationRecord{}, ErrWorkspaceRecordVersion
	}
	var record workspacecontrol.OperationRecord
	if err := decodeStrictWorkspaceJSON(payload, &record); err != nil || record.Validate() != nil {
		return workspacecontrol.OperationRecord{}, ErrWorkspaceRecordInvalid
	}
	return record, nil
}

func validWorkspaceTransition(
	current workspacecontrol.OperationRecord,
	next workspacecontrol.OperationRecord,
) bool {
	if current.OperationID != next.OperationID || current.WorktreeID != next.WorktreeID ||
		current.Fingerprint != next.Fingerprint || current.StepCount != next.StepCount ||
		next.NextStep < current.NextStep || current.State == workspacecontrol.StateReady ||
		current.State == workspacecontrol.StateFailed || next.State == workspacecontrol.StateReady {
		return false
	}
	if next.State == workspacecontrol.StateFailed {
		return next.Phase == workspacecontrol.PhaseComplete && next.FailureCode != ""
	}
	return next.State == workspacecontrol.StateRunning && next.FailureCode == ""
}

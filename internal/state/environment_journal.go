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
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/domain"
)

const environmentRecordSchemaVersion = 1

const (
	DefaultCurrentEnvironmentPageSize = 100
	MaximumCurrentEnvironmentPageSize = 1000
)

var (
	ErrEnvironmentJournalConfig     = errors.New("invalid environment journal configuration")
	ErrEnvironmentOperationMismatch = errors.New("environment journal operation does not match the public operation")
	ErrEnvironmentRecordExists      = errors.New("environment operation record already exists")
	ErrEnvironmentRecordNotFound    = errors.New("environment operation record not found")
	ErrEnvironmentRecordInvalid     = errors.New("environment operation record is invalid")
	ErrEnvironmentResultInvalid     = errors.New("environment result is invalid")
	ErrEnvironmentRecordVersion     = errors.New("environment record schema version is unsupported")
	ErrEnvironmentProjection        = errors.New("environment result projection failed")
)

type EnvironmentProjector func(
	current *contractv1.Environment,
	result environmentcontrol.EnvironmentResult,
) (contractv1.Environment, error)

type EnvironmentJournal struct {
	store     *Store
	projector EnvironmentProjector
}

type CurrentEnvironmentPage struct {
	Results           []environmentcontrol.EnvironmentResult
	NextEnvironmentID string
	HasMore           bool
}

func NewEnvironmentJournal(store *Store, projector EnvironmentProjector) (*EnvironmentJournal, error) {
	if store == nil || projector == nil {
		return nil, ErrEnvironmentJournalConfig
	}
	return &EnvironmentJournal{store: store, projector: projector}, nil
}

func (journal *EnvironmentJournal) Create(ctx context.Context, record environmentcontrol.OperationRecord) error {
	record = normalizeOperationRecord(record)
	if err := validateOperationRecord(record); err != nil || record.State != domain.OperationPending || record.Phase != environmentcontrol.PhasePending {
		return ErrEnvironmentRecordInvalid
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return ErrEnvironmentRecordInvalid
	}

	transaction, err := journal.store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin environment record creation: %w", err)
	}
	public, _, err := scanStoredOperation(transaction.QueryRowContext(ctx, operationQuery+" WHERE id = ?", record.ID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return ErrEnvironmentOperationMismatch
	}
	if err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("read public environment operation: %w", err)
	}
	if public.ID != record.ID || public.Kind != string(record.Kind) || public.EnvironmentID != record.EnvironmentID || public.State != string(domain.OperationPending) {
		_ = transaction.Rollback()
		return ErrEnvironmentOperationMismatch
	}
	var existingCount int
	if err := transaction.QueryRowContext(ctx, "SELECT COUNT(*) FROM environment_operation_records WHERE operation_id = ?", record.ID).Scan(&existingCount); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("check environment operation record: %w", err)
	}
	if existingCount != 0 {
		_ = transaction.Rollback()
		return ErrEnvironmentRecordExists
	}

	now := journal.store.now().UTC()
	_, err = transaction.ExecContext(ctx, `
INSERT INTO environment_operation_records(
    operation_id, schema_version, environment_id, run_id, kind, operation_state,
    record_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, environmentRecordSchemaVersion, record.EnvironmentID, record.RunID,
		string(record.Kind), string(record.State), payload, now.Format(timeFormat), now.Format(timeFormat),
	)
	if err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("persist environment operation record: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit environment operation record: %w", err)
	}
	return nil
}

func (journal *EnvironmentJournal) Update(ctx context.Context, record environmentcontrol.OperationRecord) error {
	record = normalizeOperationRecord(record)
	if err := validateOperationRecord(record); err != nil || terminalOperation(record.State) {
		return ErrEnvironmentRecordInvalid
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return ErrEnvironmentRecordInvalid
	}
	transaction, err := journal.store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin environment record update: %w", err)
	}
	if err := journal.verifyExisting(ctx, transaction, record); err != nil {
		_ = transaction.Rollback()
		return err
	}
	now := journal.store.now().UTC()
	if _, err := transaction.ExecContext(ctx, `
UPDATE environment_operation_records
SET operation_state = ?, record_json = ?, updated_at = ?
WHERE operation_id = ?`, string(record.State), payload, now.Format(timeFormat), record.ID); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("persist environment operation update: %w", err)
	}
	if err := updatePublicEnvironmentOperation(ctx, transaction, record, nil, now); err != nil {
		_ = transaction.Rollback()
		return err
	}
	if _, err := journal.store.commitEnvironmentJournalSnapshot(ctx, transaction, nil); err != nil {
		_ = transaction.Rollback()
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit environment operation update: %w", err)
	}
	return nil
}

func (journal *EnvironmentJournal) Publish(
	ctx context.Context,
	record environmentcontrol.OperationRecord,
	result environmentcontrol.EnvironmentResult,
) error {
	record = normalizeOperationRecord(record)
	result = normalizeEnvironmentResult(result)
	if err := validateOperationRecord(record); err != nil || !terminalOperation(record.State) || record.Phase != environmentcontrol.PhaseComplete {
		return ErrEnvironmentRecordInvalid
	}
	if err := validateEnvironmentResult(result); err != nil || result.EnvironmentID != record.EnvironmentID || result.RunID != record.RunID || result.State != record.EnvironmentState {
		return ErrEnvironmentResultInvalid
	}
	recordPayload, err := json.Marshal(record)
	if err != nil {
		return ErrEnvironmentRecordInvalid
	}
	resultPayload, err := json.Marshal(result)
	if err != nil {
		return ErrEnvironmentResultInvalid
	}

	transaction, err := journal.store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin environment result publication: %w", err)
	}
	if err := journal.verifyExisting(ctx, transaction, record); err != nil {
		_ = transaction.Rollback()
		return err
	}
	snapshot, err := readSnapshotTransaction(ctx, transaction)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	current := findEnvironment(snapshot.Environments, result.EnvironmentID)
	projected, err := journal.projector(cloneContractEnvironment(current), result)
	if err != nil {
		_ = transaction.Rollback()
		return ErrEnvironmentProjection
	}
	projected = normalizeContractEnvironment(projected)
	if projected.ID != result.EnvironmentID {
		_ = transaction.Rollback()
		return ErrEnvironmentProjection
	}
	if current == nil {
		projected.Revision = 1
	} else {
		projected.Revision = current.Revision + 1
	}
	projectedEnvironments := mergeEnvironment(snapshot.Environments, projected)
	projectedSnapshot := snapshot
	projectedSnapshot.Environments = projectedEnvironments
	if err := projectedSnapshot.Validate(); err != nil {
		_ = transaction.Rollback()
		return ErrEnvironmentProjection
	}

	now := journal.store.now().UTC()
	if _, err := transaction.ExecContext(ctx, `
UPDATE environment_operation_records
SET operation_state = ?, record_json = ?, updated_at = ?
WHERE operation_id = ?`, string(record.State), recordPayload, now.Format(timeFormat), record.ID); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("persist terminal environment operation: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO environment_current_results(environment_id, schema_version, operation_id, result_json, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(environment_id) DO UPDATE SET
    schema_version = excluded.schema_version,
    operation_id = excluded.operation_id,
    result_json = excluded.result_json,
    updated_at = excluded.updated_at`,
		result.EnvironmentID, environmentRecordSchemaVersion, record.ID, resultPayload, now.Format(timeFormat),
	); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("persist current environment result: %w", err)
	}
	if err := updatePublicEnvironmentOperation(ctx, transaction, record, &projected.Revision, now); err != nil {
		_ = transaction.Rollback()
		return err
	}
	snapshot.Environments = projectedEnvironments
	if _, err := journal.store.commitEnvironmentJournalSnapshot(ctx, transaction, &snapshot.Environments); err != nil {
		_ = transaction.Rollback()
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit environment result publication: %w", err)
	}
	return nil
}

func (journal *EnvironmentJournal) Current(ctx context.Context, environmentID string) (environmentcontrol.EnvironmentResult, bool, error) {
	if environmentID == "" {
		return environmentcontrol.EnvironmentResult{}, false, ErrEnvironmentResultInvalid
	}
	var version int
	var payload []byte
	err := journal.store.database.QueryRowContext(ctx, `
SELECT schema_version, result_json
FROM environment_current_results
WHERE environment_id = ?`, environmentID).Scan(&version, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return environmentcontrol.EnvironmentResult{}, false, nil
	}
	if err != nil {
		return environmentcontrol.EnvironmentResult{}, false, fmt.Errorf("read current environment result: %w", err)
	}
	result, err := decodeEnvironmentResult(version, payload)
	if err != nil || result.EnvironmentID != environmentID {
		if errors.Is(err, ErrEnvironmentRecordVersion) {
			return environmentcontrol.EnvironmentResult{}, false, err
		}
		return environmentcontrol.EnvironmentResult{}, false, ErrEnvironmentResultInvalid
	}
	return result, true, nil
}

func (journal *EnvironmentJournal) ListCurrent(
	ctx context.Context,
	afterEnvironmentID string,
	requestedLimit int,
) (CurrentEnvironmentPage, error) {
	limit := requestedLimit
	if limit <= 0 {
		limit = DefaultCurrentEnvironmentPageSize
	}
	if limit > MaximumCurrentEnvironmentPageSize {
		limit = MaximumCurrentEnvironmentPageSize
	}
	rows, err := journal.store.database.QueryContext(ctx, `
SELECT environment_id, schema_version, result_json
FROM environment_current_results
WHERE environment_id > ?
ORDER BY environment_id ASC
LIMIT ?`, afterEnvironmentID, limit+1)
	if err != nil {
		return CurrentEnvironmentPage{}, fmt.Errorf("list current environment results: %w", err)
	}
	defer rows.Close()
	page := CurrentEnvironmentPage{
		Results:           make([]environmentcontrol.EnvironmentResult, 0, limit),
		NextEnvironmentID: afterEnvironmentID,
	}
	for rows.Next() {
		var environmentID string
		var version int
		var payload []byte
		if err := rows.Scan(&environmentID, &version, &payload); err != nil {
			return CurrentEnvironmentPage{}, fmt.Errorf("scan current environment result: %w", err)
		}
		if len(page.Results) == limit {
			page.HasMore = true
			break
		}
		result, err := decodeEnvironmentResult(version, payload)
		if err != nil {
			return CurrentEnvironmentPage{}, err
		}
		if result.EnvironmentID != environmentID {
			return CurrentEnvironmentPage{}, ErrEnvironmentResultInvalid
		}
		page.Results = append(page.Results, result)
		page.NextEnvironmentID = environmentID
	}
	if err := rows.Err(); err != nil {
		return CurrentEnvironmentPage{}, fmt.Errorf("iterate current environment results: %w", err)
	}
	return page, nil
}

func (journal *EnvironmentJournal) Incomplete(ctx context.Context) ([]environmentcontrol.OperationRecord, error) {
	rows, err := journal.store.database.QueryContext(ctx, `
SELECT schema_version, operation_id, environment_id, run_id, kind, operation_state, record_json
FROM environment_operation_records
WHERE operation_state IN ('pending', 'running')
ORDER BY created_at ASC, operation_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("read incomplete environment operations: %w", err)
	}
	defer rows.Close()
	records := make([]environmentcontrol.OperationRecord, 0)
	for rows.Next() {
		var version int
		var operationID, environmentID, runID, kind, state string
		var payload []byte
		if err := rows.Scan(&version, &operationID, &environmentID, &runID, &kind, &state, &payload); err != nil {
			return nil, fmt.Errorf("scan incomplete environment operation: %w", err)
		}
		record, err := decodeOperationRecord(version, payload)
		if err != nil {
			return nil, err
		}
		if record.ID != operationID || record.EnvironmentID != environmentID || record.RunID != runID || string(record.Kind) != kind || string(record.State) != state {
			return nil, ErrEnvironmentRecordInvalid
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incomplete environment operations: %w", err)
	}
	return records, nil
}

func (journal *EnvironmentJournal) verifyExisting(
	ctx context.Context,
	transaction *sql.Tx,
	next environmentcontrol.OperationRecord,
) error {
	var version int
	var environmentID, runID, kind string
	var payload []byte
	err := transaction.QueryRowContext(ctx, `
SELECT schema_version, environment_id, run_id, kind, record_json
FROM environment_operation_records
WHERE operation_id = ?`, next.ID).Scan(&version, &environmentID, &runID, &kind, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEnvironmentRecordNotFound
	}
	if err != nil {
		return fmt.Errorf("read existing environment operation record: %w", err)
	}
	existing, err := decodeOperationRecord(version, payload)
	if err != nil {
		return err
	}
	if existing.ID != next.ID || existing.EnvironmentID != next.EnvironmentID || existing.RunID != next.RunID || existing.Kind != next.Kind ||
		environmentID != next.EnvironmentID || runID != next.RunID || kind != string(next.Kind) {
		return ErrEnvironmentRecordInvalid
	}
	if existing.State != next.State {
		if err := domain.ValidateOperationTransition(existing.State, next.State); err != nil {
			return ErrEnvironmentRecordInvalid
		}
	}
	if existing.EnvironmentState != next.EnvironmentState {
		if err := domain.ValidateEnvironmentTransition(existing.EnvironmentState, next.EnvironmentState); err != nil {
			return ErrEnvironmentRecordInvalid
		}
	}
	public, _, err := scanStoredOperation(transaction.QueryRowContext(ctx, operationQuery+" WHERE id = ?", next.ID))
	if err != nil {
		return ErrEnvironmentOperationMismatch
	}
	if public.Kind != string(next.Kind) || public.EnvironmentID != next.EnvironmentID || public.State != string(existing.State) {
		return ErrEnvironmentOperationMismatch
	}
	return nil
}

func updatePublicEnvironmentOperation(
	ctx context.Context,
	transaction *sql.Tx,
	record environmentcontrol.OperationRecord,
	environmentRevision *int64,
	updatedAt time.Time,
) error {
	errorPayload, err := publicOperationError(record)
	if err != nil {
		return err
	}
	var revision any
	if environmentRevision != nil {
		revision = *environmentRevision
	} else {
		var current sql.NullInt64
		if err := transaction.QueryRowContext(ctx, "SELECT environment_revision FROM operations WHERE id = ?", record.ID).Scan(&current); err != nil {
			return ErrEnvironmentOperationMismatch
		}
		if current.Valid {
			revision = current.Int64
		}
	}
	result, err := transaction.ExecContext(ctx, `
UPDATE operations
SET state = ?, environment_revision = ?, updated_at = ?, error_json = ?
WHERE id = ? AND kind = ? AND environment_id = ?`,
		string(record.State), revision, updatedAt.Format(timeFormat), errorPayload,
		record.ID, string(record.Kind), record.EnvironmentID,
	)
	if err != nil {
		return fmt.Errorf("update public environment operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrEnvironmentOperationMismatch
	}
	return nil
}

func publicOperationError(record environmentcontrol.OperationRecord) (any, error) {
	var publicError *contractv1.ContractError
	switch record.State {
	case domain.OperationFailed:
		publicError = &contractv1.ContractError{
			Code: "ENVIRONMENT_OPERATION_FAILED", Message: "Environment operation failed.", Retryable: true,
		}
	case domain.OperationCancelled:
		publicError = &contractv1.ContractError{
			Code: "ENVIRONMENT_OPERATION_CANCELLED", Message: "Environment operation was cancelled.", Retryable: true,
		}
	}
	if publicError == nil {
		return nil, nil
	}
	payload, err := json.Marshal(publicError)
	if err != nil {
		return nil, ErrEnvironmentRecordInvalid
	}
	return payload, nil
}

func (store *Store) commitEnvironmentJournalSnapshot(
	ctx context.Context,
	transaction *sql.Tx,
	environments *[]contractv1.Environment,
) (contractv1.StatusSnapshot, error) {
	snapshot, err := readSnapshotTransaction(ctx, transaction)
	if err != nil {
		return contractv1.StatusSnapshot{}, err
	}
	operations, err := listOperations(ctx, transaction)
	if err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("refresh environment operation snapshot: %w", err)
	}
	snapshot.Operations = operations
	if environments != nil {
		snapshot.Environments = append([]contractv1.Environment(nil), (*environments)...)
	}
	return store.commitSnapshotTransaction(ctx, transaction, snapshot)
}

func readSnapshotTransaction(ctx context.Context, transaction *sql.Tx) (contractv1.StatusSnapshot, error) {
	var revision int64
	var payload []byte
	err := transaction.QueryRowContext(ctx, `
SELECT snapshot_head.revision, snapshot_revisions.payload_json
FROM snapshot_head
JOIN snapshot_revisions ON snapshot_revisions.revision = snapshot_head.revision
WHERE snapshot_head.singleton = 1`).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return contractv1.StatusSnapshot{}, ErrNoSnapshot
	}
	if err != nil {
		return contractv1.StatusSnapshot{}, fmt.Errorf("read environment journal snapshot: %w", err)
	}
	var snapshot contractv1.StatusSnapshot
	if err := decodeStrict(payload, &snapshot); err != nil || snapshot.SnapshotRevision != revision || snapshot.Validate() != nil {
		return contractv1.StatusSnapshot{}, errors.New("stored status snapshot is invalid")
	}
	return snapshot, nil
}

func findEnvironment(environments []contractv1.Environment, environmentID string) *contractv1.Environment {
	for index := range environments {
		if environments[index].ID == environmentID {
			copy := environments[index]
			return &copy
		}
	}
	return nil
}

func mergeEnvironment(environments []contractv1.Environment, projected contractv1.Environment) []contractv1.Environment {
	merged := append([]contractv1.Environment(nil), environments...)
	replaced := false
	for index := range merged {
		if merged[index].ID == projected.ID {
			merged[index] = projected
			replaced = true
			break
		}
	}
	if !replaced {
		merged = append(merged, projected)
	}
	sort.Slice(merged, func(left, right int) bool { return merged[left].ID < merged[right].ID })
	return merged
}

func decodeOperationRecord(version int, payload []byte) (environmentcontrol.OperationRecord, error) {
	if version != environmentRecordSchemaVersion {
		return environmentcontrol.OperationRecord{}, ErrEnvironmentRecordVersion
	}
	var record environmentcontrol.OperationRecord
	if err := decodeStrict(payload, &record); err != nil {
		return environmentcontrol.OperationRecord{}, ErrEnvironmentRecordInvalid
	}
	if err := validateOperationRecord(record); err != nil {
		return environmentcontrol.OperationRecord{}, ErrEnvironmentRecordInvalid
	}
	return record, nil
}

func decodeEnvironmentResult(version int, payload []byte) (environmentcontrol.EnvironmentResult, error) {
	if version != environmentRecordSchemaVersion {
		return environmentcontrol.EnvironmentResult{}, ErrEnvironmentRecordVersion
	}
	var result environmentcontrol.EnvironmentResult
	if err := decodeStrict(payload, &result); err != nil {
		return environmentcontrol.EnvironmentResult{}, ErrEnvironmentResultInvalid
	}
	if err := validateEnvironmentResult(result); err != nil {
		return environmentcontrol.EnvironmentResult{}, ErrEnvironmentResultInvalid
	}
	return result, nil
}

func decodeStrict(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON payload contains trailing data")
	}
	return nil
}

func terminalOperation(state domain.OperationState) bool {
	return state == domain.OperationSucceeded || state == domain.OperationFailed || state == domain.OperationCancelled
}

var _ environmentcontrol.OperationJournal = (*EnvironmentJournal)(nil)

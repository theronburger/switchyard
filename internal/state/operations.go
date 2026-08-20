package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/events"
)

var (
	ErrIdempotencyConflict         = errors.New("idempotency key was already used for a different request")
	ErrEnvironmentBusy             = errors.New("environment already has an active operation")
	ErrEnvironmentRevisionConflict = errors.New("environment revision does not match the mutation precondition")
	ErrOperationNotFound           = errors.New("operation not found")
	ErrInvalidOperationTransition  = errors.New("invalid operation transition")
)

type NewOperation struct {
	ID                          string
	RunID                       string
	RequestID                   string
	IdempotencyKey              string
	RequestFingerprint          [sha256.Size]byte
	Kind                        string
	EnvironmentID               string
	ExpectedEnvironmentRevision *int64
}

func FingerprintRequest(request any) ([sha256.Size]byte, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode operation request: %w", err)
	}
	return sha256.Sum256(payload), nil
}

func (store *Store) CreateOperation(ctx context.Context, request NewOperation) (contractv2.Operation, bool, error) {
	if request.ID == "" {
		return contractv2.Operation{}, false, errors.New("operation id is required")
	}
	if request.RequestID == "" {
		return contractv2.Operation{}, false, errors.New("request id is required")
	}
	if request.IdempotencyKey == "" {
		return contractv2.Operation{}, false, errors.New("idempotency key is required")
	}
	if request.Kind == "" {
		return contractv2.Operation{}, false, errors.New("operation kind is required")
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv2.Operation{}, false, fmt.Errorf("begin operation: %w", err)
	}

	existing, existingFingerprint, err := readOperationByIdempotencyKey(ctx, transaction, request.IdempotencyKey)
	if err == nil {
		_ = transaction.Rollback()
		if !bytes.Equal(existingFingerprint, request.RequestFingerprint[:]) {
			return contractv2.Operation{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return contractv2.Operation{}, false, fmt.Errorf("read idempotent operation: %w", err)
	}
	if request.EnvironmentID != "" {
		var activeOperationID string
		err := transaction.QueryRowContext(ctx, `
SELECT id
FROM operations
WHERE environment_id = ? AND state IN ('pending', 'running')
ORDER BY created_at, id
LIMIT 1`, request.EnvironmentID).Scan(&activeOperationID)
		if err == nil {
			_ = transaction.Rollback()
			return contractv2.Operation{}, false, ErrEnvironmentBusy
		}
		if !errors.Is(err, sql.ErrNoRows) {
			_ = transaction.Rollback()
			return contractv2.Operation{}, false, fmt.Errorf("read active environment operation: %w", err)
		}
	}
	if request.ExpectedEnvironmentRevision != nil {
		if request.EnvironmentID == "" {
			_ = transaction.Rollback()
			return contractv2.Operation{}, false, errors.New("expected environment revision requires an environment id")
		}
		snapshot, err := readSnapshotTransaction(ctx, transaction)
		if err != nil {
			_ = transaction.Rollback()
			return contractv2.Operation{}, false, fmt.Errorf("read environment revision precondition: %w", err)
		}
		currentRevision := int64(0)
		for _, environment := range snapshot.Environments {
			if environment.ID == request.EnvironmentID {
				currentRevision = environment.Revision
				break
			}
		}
		if currentRevision != *request.ExpectedEnvironmentRevision {
			_ = transaction.Rollback()
			return contractv2.Operation{}, false, ErrEnvironmentRevisionConflict
		}
	}

	now := store.now().UTC()
	phase := ""
	if request.EnvironmentID != "" {
		phase = "pending"
	}
	var environmentRevision any
	if request.ExpectedEnvironmentRevision != nil {
		environmentRevision = *request.ExpectedEnvironmentRevision
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO operations(
    id, run_id, request_id, idempotency_key, request_fingerprint, kind, state,
    phase, environment_id, environment_revision, created_at, updated_at
) VALUES (?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)`,
		request.ID,
		request.RunID,
		request.RequestID,
		request.IdempotencyKey,
		request.RequestFingerprint[:],
		request.Kind,
		"pending",
		phase,
		request.EnvironmentID,
		environmentRevision,
		now.Format(timeFormat),
		now.Format(timeFormat),
	); err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, false, fmt.Errorf("persist operation: %w", err)
	}
	if err := pruneTerminalOperations(ctx, transaction); err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, false, err
	}
	if err := store.commitOperationsSnapshot(ctx, transaction); err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, false, err
	}
	if err := store.recordAuditEvent(ctx, transaction, events.KindOperationCreated, request.EnvironmentID, events.OperationAuditPayload{
		OperationID: request.ID, Kind: request.Kind, State: "pending", RunID: request.RunID, EnvironmentID: request.EnvironmentID,
	}); err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv2.Operation{}, false, fmt.Errorf("commit operation: %w", err)
	}

	operation := contractv2.Operation{
		ID:            request.ID,
		RunID:         request.RunID,
		Kind:          request.Kind,
		State:         "pending",
		Phase:         phase,
		EnvironmentID: request.EnvironmentID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if request.ExpectedEnvironmentRevision != nil {
		operation.EnvironmentRevision = *request.ExpectedEnvironmentRevision
	}
	return operation, true, nil
}

func (store *Store) ReadOperation(ctx context.Context, operationID string) (contractv2.Operation, error) {
	operation, _, err := scanStoredOperation(store.database.QueryRowContext(ctx, operationQuery+" WHERE id = ?", operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return contractv2.Operation{}, ErrOperationNotFound
	}
	if err != nil {
		return contractv2.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	return operation, nil
}

func (store *Store) ListOperations(ctx context.Context) ([]contractv2.Operation, error) {
	operations, err := listOperations(ctx, store.database)
	if err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	return operations, nil
}

func (store *Store) FailInterruptedOperations(
	ctx context.Context,
	operationError contractv2.ContractError,
) ([]contractv2.Operation, error) {
	if operationError.Code == "" || operationError.Message == "" {
		return nil, errors.New("interrupted operation error code and message are required")
	}
	errorPayload, err := json.Marshal(operationError)
	if err != nil {
		return nil, fmt.Errorf("encode interrupted operation error: %w", err)
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin interrupted operation reconciliation: %w", err)
	}
	operations, err := listOperations(ctx, transaction)
	if err != nil {
		_ = transaction.Rollback()
		return nil, fmt.Errorf("list interrupted operations: %w", err)
	}
	environmentOwned, err := incompleteEnvironmentOperationIDs(ctx, transaction)
	if err != nil {
		_ = transaction.Rollback()
		return nil, fmt.Errorf("list environment-owned operations: %w", err)
	}

	updatedAt := store.now().UTC()
	interrupted := make([]contractv2.Operation, 0)
	for _, operation := range operations {
		if operation.State != "pending" && operation.State != "running" {
			continue
		}
		if _, owned := environmentOwned[operation.ID]; owned {
			continue
		}
		if _, err := transaction.ExecContext(
			ctx,
			"UPDATE operations SET state = 'failed', updated_at = ?, error_json = ? WHERE id = ?",
			updatedAt.Format(timeFormat),
			errorPayload,
			operation.ID,
		); err != nil {
			_ = transaction.Rollback()
			return nil, fmt.Errorf("fail interrupted operation: %w", err)
		}
		operation.State = "failed"
		operation.UpdatedAt = updatedAt
		operationErrorCopy := operationError
		operation.Error = &operationErrorCopy
		interrupted = append(interrupted, operation)
		if err := store.recordAuditEvent(ctx, transaction, events.KindOperationTransitioned, operation.EnvironmentID, operationAuditPayload(operation)); err != nil {
			_ = transaction.Rollback()
			return nil, err
		}
	}
	if len(interrupted) == 0 {
		_ = transaction.Rollback()
		return []contractv2.Operation{}, nil
	}
	if err := store.commitOperationsSnapshot(ctx, transaction); err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit interrupted operation reconciliation: %w", err)
	}
	return interrupted, nil
}

func incompleteEnvironmentOperationIDs(ctx context.Context, transaction *sql.Tx) (map[string]struct{}, error) {
	rows, err := transaction.QueryContext(ctx, `
SELECT operation_id
FROM environment_operation_records
WHERE operation_state IN ('pending', 'running')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	owned := make(map[string]struct{})
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			return nil, err
		}
		owned[operationID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return owned, nil
}

func (store *Store) TransitionOperation(
	ctx context.Context,
	operationID string,
	nextState string,
	operationError *contractv2.ContractError,
) (contractv2.Operation, error) {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv2.Operation{}, fmt.Errorf("begin operation transition: %w", err)
	}

	operation, _, err := scanStoredOperation(transaction.QueryRowContext(ctx, operationQuery+" WHERE id = ?", operationID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return contractv2.Operation{}, ErrOperationNotFound
	}
	if err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, fmt.Errorf("read operation for transition: %w", err)
	}
	if operation.State == nextState {
		_ = transaction.Rollback()
		return operation, nil
	}
	if !operationTransitionAllowed(operation.State, nextState) {
		_ = transaction.Rollback()
		return contractv2.Operation{}, fmt.Errorf("%w: %s to %s", ErrInvalidOperationTransition, operation.State, nextState)
	}
	if nextState == "failed" && operationError == nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, errors.New("failed operation requires an error")
	}

	var errorPayload any
	if operationError != nil {
		encodedError, err := json.Marshal(operationError)
		if err != nil {
			_ = transaction.Rollback()
			return contractv2.Operation{}, fmt.Errorf("encode operation error: %w", err)
		}
		errorPayload = encodedError
	}
	updatedAt := store.now().UTC()
	if _, err := transaction.ExecContext(
		ctx,
		"UPDATE operations SET state = ?, updated_at = ?, error_json = ? WHERE id = ?",
		nextState,
		updatedAt.Format(timeFormat),
		errorPayload,
		operationID,
	); err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, fmt.Errorf("persist operation transition: %w", err)
	}
	if err := store.commitOperationsSnapshot(ctx, transaction); err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, err
	}
	operation.State = nextState
	operation.UpdatedAt = updatedAt
	operation.Error = operationError
	if err := store.recordAuditEvent(ctx, transaction, events.KindOperationTransitioned, operation.EnvironmentID, operationAuditPayload(operation)); err != nil {
		_ = transaction.Rollback()
		return contractv2.Operation{}, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv2.Operation{}, fmt.Errorf("commit operation transition: %w", err)
	}
	return operation, nil
}

func operationAuditPayload(operation contractv2.Operation) events.OperationAuditPayload {
	payload := events.OperationAuditPayload{
		OperationID: operation.ID, Kind: operation.Kind, State: operation.State,
		RunID: operation.RunID, EnvironmentID: operation.EnvironmentID,
	}
	if operation.Error != nil {
		payload.ErrorCode = operation.Error.Code
	}
	return payload
}

func operationTransitionAllowed(currentState, nextState string) bool {
	switch currentState {
	case "pending":
		return nextState == "running" || nextState == "failed" || nextState == "cancelled"
	case "running":
		return nextState == "succeeded" || nextState == "failed" || nextState == "cancelled"
	default:
		return false
	}
}

const operationQuery = `
SELECT id, run_id, request_fingerprint, kind, state, phase, environment_id, environment_revision,
       created_at, updated_at, error_json
FROM operations`

type operationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listOperations(ctx context.Context, queryer operationQueryer) ([]contractv2.Operation, error) {
	rows, err := queryer.QueryContext(ctx, operationQuery+" ORDER BY created_at ASC, id ASC")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	operations := make([]contractv2.Operation, 0)
	for rows.Next() {
		operation, _, err := scanStoredOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return operations, nil
}

type rowScanner interface {
	Scan(destinations ...any) error
}

func readOperationByIdempotencyKey(
	ctx context.Context,
	transaction *sql.Tx,
	idempotencyKey string,
) (contractv2.Operation, []byte, error) {
	return scanStoredOperation(transaction.QueryRowContext(ctx, operationQuery+" WHERE idempotency_key = ?", idempotencyKey))
}

func scanStoredOperation(row rowScanner) (contractv2.Operation, []byte, error) {
	var operation contractv2.Operation
	var fingerprint []byte
	var runID sql.NullString
	var environmentID sql.NullString
	var environmentRevision sql.NullInt64
	var createdAt string
	var updatedAt string
	var errorPayload []byte
	if err := row.Scan(
		&operation.ID,
		&runID,
		&fingerprint,
		&operation.Kind,
		&operation.State,
		&operation.Phase,
		&environmentID,
		&environmentRevision,
		&createdAt,
		&updatedAt,
		&errorPayload,
	); err != nil {
		return contractv2.Operation{}, nil, err
	}

	parsedCreatedAt, err := time.Parse(timeFormat, createdAt)
	if err != nil {
		return contractv2.Operation{}, nil, fmt.Errorf("parse operation creation time: %w", err)
	}
	parsedUpdatedAt, err := time.Parse(timeFormat, updatedAt)
	if err != nil {
		return contractv2.Operation{}, nil, fmt.Errorf("parse operation update time: %w", err)
	}
	operation.EnvironmentID = environmentID.String
	operation.RunID = runID.String
	operation.EnvironmentRevision = environmentRevision.Int64
	operation.CreatedAt = parsedCreatedAt
	operation.UpdatedAt = parsedUpdatedAt
	if len(errorPayload) > 0 {
		var operationError contractv2.ContractError
		if err := json.Unmarshal(errorPayload, &operationError); err != nil {
			return contractv2.Operation{}, nil, fmt.Errorf("decode operation error: %w", err)
		}
		operation.Error = &operationError
	}
	return operation, fingerprint, nil
}

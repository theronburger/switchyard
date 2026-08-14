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

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

var (
	ErrIdempotencyConflict        = errors.New("idempotency key was already used for a different request")
	ErrOperationNotFound          = errors.New("operation not found")
	ErrInvalidOperationTransition = errors.New("invalid operation transition")
)

type NewOperation struct {
	ID                          string
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

func (store *Store) CreateOperation(ctx context.Context, request NewOperation) (contractv1.Operation, bool, error) {
	if request.ID == "" {
		return contractv1.Operation{}, false, errors.New("operation id is required")
	}
	if request.RequestID == "" {
		return contractv1.Operation{}, false, errors.New("request id is required")
	}
	if request.IdempotencyKey == "" {
		return contractv1.Operation{}, false, errors.New("idempotency key is required")
	}
	if request.Kind == "" {
		return contractv1.Operation{}, false, errors.New("operation kind is required")
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv1.Operation{}, false, fmt.Errorf("begin operation: %w", err)
	}

	existing, existingFingerprint, err := readOperationByIdempotencyKey(ctx, transaction, request.IdempotencyKey)
	if err == nil {
		_ = transaction.Rollback()
		if !bytes.Equal(existingFingerprint, request.RequestFingerprint[:]) {
			return contractv1.Operation{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return contractv1.Operation{}, false, fmt.Errorf("read idempotent operation: %w", err)
	}

	now := store.now().UTC()
	var environmentRevision any
	if request.ExpectedEnvironmentRevision != nil {
		environmentRevision = *request.ExpectedEnvironmentRevision
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO operations(
    id, request_id, idempotency_key, request_fingerprint, kind, state,
    environment_id, environment_revision, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?)`,
		request.ID,
		request.RequestID,
		request.IdempotencyKey,
		request.RequestFingerprint[:],
		request.Kind,
		"pending",
		request.EnvironmentID,
		environmentRevision,
		now.Format(timeFormat),
		now.Format(timeFormat),
	); err != nil {
		_ = transaction.Rollback()
		return contractv1.Operation{}, false, fmt.Errorf("persist operation: %w", err)
	}
	if err := store.commitOperationsSnapshot(ctx, transaction); err != nil {
		_ = transaction.Rollback()
		return contractv1.Operation{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv1.Operation{}, false, fmt.Errorf("commit operation: %w", err)
	}

	operation := contractv1.Operation{
		ID:            request.ID,
		Kind:          request.Kind,
		State:         "pending",
		EnvironmentID: request.EnvironmentID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if request.ExpectedEnvironmentRevision != nil {
		operation.EnvironmentRevision = *request.ExpectedEnvironmentRevision
	}
	return operation, true, nil
}

func (store *Store) ReadOperation(ctx context.Context, operationID string) (contractv1.Operation, error) {
	operation, _, err := scanStoredOperation(store.database.QueryRowContext(ctx, operationQuery+" WHERE id = ?", operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return contractv1.Operation{}, ErrOperationNotFound
	}
	if err != nil {
		return contractv1.Operation{}, fmt.Errorf("read operation: %w", err)
	}
	return operation, nil
}

func (store *Store) ListOperations(ctx context.Context) ([]contractv1.Operation, error) {
	operations, err := listOperations(ctx, store.database)
	if err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	return operations, nil
}

func (store *Store) TransitionOperation(
	ctx context.Context,
	operationID string,
	nextState string,
	operationError *contractv1.ContractError,
) (contractv1.Operation, error) {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv1.Operation{}, fmt.Errorf("begin operation transition: %w", err)
	}

	operation, _, err := scanStoredOperation(transaction.QueryRowContext(ctx, operationQuery+" WHERE id = ?", operationID))
	if errors.Is(err, sql.ErrNoRows) {
		_ = transaction.Rollback()
		return contractv1.Operation{}, ErrOperationNotFound
	}
	if err != nil {
		_ = transaction.Rollback()
		return contractv1.Operation{}, fmt.Errorf("read operation for transition: %w", err)
	}
	if operation.State == nextState {
		_ = transaction.Rollback()
		return operation, nil
	}
	if !operationTransitionAllowed(operation.State, nextState) {
		_ = transaction.Rollback()
		return contractv1.Operation{}, fmt.Errorf("%w: %s to %s", ErrInvalidOperationTransition, operation.State, nextState)
	}
	if nextState == "failed" && operationError == nil {
		_ = transaction.Rollback()
		return contractv1.Operation{}, errors.New("failed operation requires an error")
	}

	var errorPayload any
	if operationError != nil {
		encodedError, err := json.Marshal(operationError)
		if err != nil {
			_ = transaction.Rollback()
			return contractv1.Operation{}, fmt.Errorf("encode operation error: %w", err)
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
		return contractv1.Operation{}, fmt.Errorf("persist operation transition: %w", err)
	}
	if err := store.commitOperationsSnapshot(ctx, transaction); err != nil {
		_ = transaction.Rollback()
		return contractv1.Operation{}, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv1.Operation{}, fmt.Errorf("commit operation transition: %w", err)
	}

	operation.State = nextState
	operation.UpdatedAt = updatedAt
	operation.Error = operationError
	return operation, nil
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
SELECT id, request_fingerprint, kind, state, environment_id, environment_revision,
       created_at, updated_at, error_json
FROM operations`

type operationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listOperations(ctx context.Context, queryer operationQueryer) ([]contractv1.Operation, error) {
	rows, err := queryer.QueryContext(ctx, operationQuery+" ORDER BY created_at ASC, id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	operations := make([]contractv1.Operation, 0)
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
) (contractv1.Operation, []byte, error) {
	return scanStoredOperation(transaction.QueryRowContext(ctx, operationQuery+" WHERE idempotency_key = ?", idempotencyKey))
}

func scanStoredOperation(row rowScanner) (contractv1.Operation, []byte, error) {
	var operation contractv1.Operation
	var fingerprint []byte
	var environmentID sql.NullString
	var environmentRevision sql.NullInt64
	var createdAt string
	var updatedAt string
	var errorPayload []byte
	if err := row.Scan(
		&operation.ID,
		&fingerprint,
		&operation.Kind,
		&operation.State,
		&environmentID,
		&environmentRevision,
		&createdAt,
		&updatedAt,
		&errorPayload,
	); err != nil {
		return contractv1.Operation{}, nil, err
	}

	parsedCreatedAt, err := time.Parse(timeFormat, createdAt)
	if err != nil {
		return contractv1.Operation{}, nil, fmt.Errorf("parse operation creation time: %w", err)
	}
	parsedUpdatedAt, err := time.Parse(timeFormat, updatedAt)
	if err != nil {
		return contractv1.Operation{}, nil, fmt.Errorf("parse operation update time: %w", err)
	}
	operation.EnvironmentID = environmentID.String
	operation.EnvironmentRevision = environmentRevision.Int64
	operation.CreatedAt = parsedCreatedAt
	operation.UpdatedAt = parsedUpdatedAt
	if len(errorPayload) > 0 {
		var operationError contractv1.ContractError
		if err := json.Unmarshal(errorPayload, &operationError); err != nil {
			return contractv1.Operation{}, nil, fmt.Errorf("decode operation error: %w", err)
		}
		operation.Error = &operationError
	}
	return operation, fingerprint, nil
}

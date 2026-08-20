package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	cleanupcontrol "github.com/theronburger/switchyard/internal/control/cleanup"
)

var (
	ErrCleanupPlanNotFound = errors.New("cleanup plan was not found")
	ErrCleanupPlanConsumed = errors.New("cleanup plan was already consumed")
	ErrCleanupPlanExpired  = errors.New("cleanup plan expired")
)

func (store *Store) SaveCleanupPlan(ctx context.Context, plan cleanupcontrol.Plan) (cleanupcontrol.Plan, error) {
	if plan.SchemaVersion != 1 || plan.ID == "" || plan.Revision != 0 || plan.CreatedAt.IsZero() ||
		plan.ExpiresAt.IsZero() || !plan.ExpiresAt.After(plan.CreatedAt) || plan.Candidates == nil || plan.Protected == nil {
		return cleanupcontrol.Plan{}, errors.New("cleanup plan is invalid")
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("begin cleanup plan: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM cleanup_plans WHERE consumed_at IS NOT NULL OR expires_at <= ?`,
		store.now().UTC().Format(timeFormat)); err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("prune cleanup plans: %w", err)
	}
	placeholder, err := json.Marshal(plan)
	if err != nil {
		return cleanupcontrol.Plan{}, err
	}
	result, err := transaction.ExecContext(ctx, `
INSERT INTO cleanup_plans(id, schema_version, plan_json, created_at, expires_at)
VALUES (?, ?, ?, ?, ?)`, plan.ID, plan.SchemaVersion, placeholder,
		plan.CreatedAt.UTC().Format(timeFormat), plan.ExpiresAt.UTC().Format(timeFormat))
	if err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("persist cleanup plan: %w", err)
	}
	revision, err := result.LastInsertId()
	if err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("read cleanup plan revision: %w", err)
	}
	plan.Revision = revision
	payload, err := json.Marshal(plan)
	if err != nil {
		return cleanupcontrol.Plan{}, err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE cleanup_plans SET plan_json = ? WHERE revision = ?`, payload, revision); err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("finalize cleanup plan: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("commit cleanup plan: %w", err)
	}
	return plan, nil
}

func (store *Store) ReadCleanupPlan(ctx context.Context, id string, expectedRevision int64) (cleanupcontrol.Plan, error) {
	var revision int64
	var payload []byte
	var expiresAt string
	var consumedAt sql.NullString
	err := store.database.QueryRowContext(ctx, `
SELECT revision, plan_json, expires_at, consumed_at FROM cleanup_plans WHERE id = ?`, id).
		Scan(&revision, &payload, &expiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return cleanupcontrol.Plan{}, ErrCleanupPlanNotFound
	}
	if err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("read cleanup plan: %w", err)
	}
	if revision != expectedRevision {
		return cleanupcontrol.Plan{}, ErrCleanupPlanNotFound
	}
	if consumedAt.Valid {
		return cleanupcontrol.Plan{}, ErrCleanupPlanConsumed
	}
	expiration, err := time.Parse(timeFormat, expiresAt)
	if err != nil {
		return cleanupcontrol.Plan{}, fmt.Errorf("parse cleanup expiration: %w", err)
	}
	if !store.now().UTC().Before(expiration) {
		return cleanupcontrol.Plan{}, ErrCleanupPlanExpired
	}
	var plan cleanupcontrol.Plan
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&plan)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || plan.ID != id || plan.Revision != revision {
		return cleanupcontrol.Plan{}, errors.New("stored cleanup plan is invalid")
	}
	return plan, nil
}

func (store *Store) ConsumeCleanupPlan(ctx context.Context, id string, revision int64) error {
	result, err := store.database.ExecContext(ctx, `
UPDATE cleanup_plans SET consumed_at = ?
WHERE id = ? AND revision = ? AND consumed_at IS NULL AND expires_at > ?`,
		store.now().UTC().Format(timeFormat), id, revision, store.now().UTC().Format(timeFormat))
	if err != nil {
		return fmt.Errorf("consume cleanup plan: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrCleanupPlanConsumed
	}
	return nil
}

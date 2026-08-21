package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	cleanupcontrol "github.com/theronburger/switchyard/internal/control/cleanup"
)

var (
	ErrCleanupPlanNotFound = errors.New("cleanup plan was not found")
	ErrCleanupPlanConsumed = errors.New("cleanup plan was already consumed")
	ErrCleanupPlanExpired  = errors.New("cleanup plan expired")
	// ErrCleanupApplyMismatch reports a retry that names the claimed plan
	// revision but a different candidate list; a claim authorizes exactly
	// one request shape.
	ErrCleanupApplyMismatch = errors.New("cleanup apply does not match the claimed request")
	ErrCleanupApplyStale    = errors.New("cleanup apply journal is stale")
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
	// Expired plans are pruned unless an apply claimed them and has not
	// finished: an interrupted cleanup is never forgotten merely because its
	// plan aged out. Consumed plans stay until expiry so a retried apply can
	// replay its recorded result deterministically.
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM cleanup_plans
WHERE expires_at <= ?
  AND revision NOT IN (SELECT plan_revision FROM cleanup_applies WHERE completed_at IS NULL)`,
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
	return decodeCleanupPlan(payload, id, revision)
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

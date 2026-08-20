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

// ClaimCleanupApply atomically claims authorization to apply exactly one plan
// revision to exactly one candidate list. The first caller creates the claim;
// a caller that repeats the same request while the claim is incomplete
// resumes it with an incremented attempt count; a caller that repeats the
// same request after completion receives the completed claim so the result
// can be replayed; any other candidate list is refused. Nothing is mutated on
// disk by this call, so a refused claim never leaves partial cleanup behind.
func (store *Store) ClaimCleanupApply(ctx context.Context, id string, expectedRevision int64, candidateIDs []string) (cleanupcontrol.Plan, cleanupcontrol.Claim, error) {
	if id == "" || expectedRevision < 1 || candidateIDs == nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, errors.New("cleanup claim request is invalid")
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, fmt.Errorf("begin cleanup claim: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	var revision int64
	var payload []byte
	var expiresAt string
	var consumedAt sql.NullString
	err = transaction.QueryRowContext(ctx, `
SELECT revision, plan_json, expires_at, consumed_at FROM cleanup_plans WHERE id = ?`, id).
		Scan(&revision, &payload, &expiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, ErrCleanupPlanNotFound
	}
	if err != nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, fmt.Errorf("read cleanup plan: %w", err)
	}
	if revision != expectedRevision {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, ErrCleanupPlanNotFound
	}
	plan, err := decodeCleanupPlan(payload, id, revision)
	if err != nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, err
	}

	var claimPayload []byte
	var completedAt sql.NullString
	err = transaction.QueryRowContext(ctx, `
SELECT claim_json, completed_at FROM cleanup_applies WHERE plan_revision = ?`, revision).Scan(&claimPayload, &completedAt)
	now := store.now().UTC()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if consumedAt.Valid {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, ErrCleanupPlanConsumed
		}
		expiration, parseErr := time.Parse(timeFormat, expiresAt)
		if parseErr != nil {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, fmt.Errorf("parse cleanup expiration: %w", parseErr)
		}
		if !now.Before(expiration) {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, ErrCleanupPlanExpired
		}
		claim, claimErr := cleanupcontrol.NewClaim(id, revision, candidateIDs, now)
		if claimErr != nil {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, claimErr
		}
		encoded, encodeErr := json.Marshal(claim)
		if encodeErr != nil {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, encodeErr
		}
		if _, execErr := transaction.ExecContext(ctx, `
INSERT INTO cleanup_applies(plan_revision, plan_id, claim_json, claimed_at, updated_at)
VALUES (?, ?, ?, ?, ?)`, revision, id, encoded, now.Format(timeFormat), now.Format(timeFormat)); execErr != nil {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, fmt.Errorf("claim cleanup apply: %w", execErr)
		}
		if commitErr := transaction.Commit(); commitErr != nil {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, fmt.Errorf("commit cleanup claim: %w", commitErr)
		}
		return plan, claim, nil
	case err != nil:
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, fmt.Errorf("read cleanup claim: %w", err)
	}

	claim, err := decodeCleanupClaim(claimPayload, id, revision)
	if err != nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, err
	}
	if claim.Completed() != completedAt.Valid {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, errors.New("stored cleanup claim is invalid")
	}
	matches := claim.Matches(id, revision, candidateIDs)
	switch {
	case claim.Completed() && matches:
		return plan, claim, nil
	case claim.Completed():
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, ErrCleanupPlanConsumed
	case !matches:
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, ErrCleanupApplyMismatch
	}
	claim, err = claim.Retry()
	if err != nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, err
	}
	if err := store.writeCleanupClaim(ctx, transaction, claim, now); err != nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, err
	}
	if err := transaction.Commit(); err != nil {
		return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, fmt.Errorf("commit cleanup claim retry: %w", err)
	}
	return plan, claim, nil
}

// RecordCleanupApply journals claim progress (the in-flight candidate or a
// newly final outcome). It refuses to rewrite a completed claim.
func (store *Store) RecordCleanupApply(ctx context.Context, claim cleanupcontrol.Claim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if claim.Completed() {
		return cleanupcontrol.ErrClaimCompleted
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin cleanup journal: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := store.writeCleanupClaim(ctx, transaction, claim, store.now().UTC()); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit cleanup journal: %w", err)
	}
	return nil
}

// CompleteCleanupApply closes the claim and consumes the plan in one
// transaction, so a plan is never consumed without its recorded outcomes and
// never left open once its outcomes are final.
func (store *Store) CompleteCleanupApply(ctx context.Context, claim cleanupcontrol.Claim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	if !claim.Completed() {
		return cleanupcontrol.ErrClaimInvalid
	}
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin cleanup completion: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := store.writeCleanupClaim(ctx, transaction, claim, claim.CompletedAt); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
UPDATE cleanup_plans SET consumed_at = ? WHERE id = ? AND revision = ? AND consumed_at IS NULL`,
		claim.CompletedAt.UTC().Format(timeFormat), claim.PlanID, claim.PlanRevision)
	if err != nil {
		return fmt.Errorf("consume cleanup plan: %w", err)
	}
	if updated, err := result.RowsAffected(); err != nil || updated != 1 {
		return ErrCleanupPlanConsumed
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit cleanup completion: %w", err)
	}
	return nil
}

func (store *Store) writeCleanupClaim(ctx context.Context, transaction *sql.Tx, claim cleanupcontrol.Claim, updatedAt time.Time) error {
	encoded, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	var completedAt any
	if claim.Completed() {
		completedAt = claim.CompletedAt.UTC().Format(timeFormat)
	}
	result, err := transaction.ExecContext(ctx, `
UPDATE cleanup_applies SET claim_json = ?, updated_at = ?, completed_at = ?
WHERE plan_revision = ? AND plan_id = ? AND completed_at IS NULL`,
		encoded, updatedAt.UTC().Format(timeFormat), completedAt, claim.PlanRevision, claim.PlanID)
	if err != nil {
		return fmt.Errorf("journal cleanup claim: %w", err)
	}
	if updated, err := result.RowsAffected(); err != nil || updated != 1 {
		return ErrCleanupApplyStale
	}
	return nil
}

func decodeCleanupPlan(payload []byte, id string, revision int64) (cleanupcontrol.Plan, error) {
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

func decodeCleanupClaim(payload []byte, id string, revision int64) (cleanupcontrol.Claim, error) {
	var claim cleanupcontrol.Claim
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&claim)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || claim.Validate() != nil || claim.PlanID != id || claim.PlanRevision != revision {
		return cleanupcontrol.Claim{}, errors.New("stored cleanup claim is invalid")
	}
	return claim, nil
}

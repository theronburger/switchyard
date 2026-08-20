package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/events"
)

var (
	ErrOccupancyLeaseNotFound   = errors.New("occupancy lease not found")
	ErrOccupancyLimit           = errors.New("worktree already holds the maximum number of occupancy leases")
	ErrOccupancyRequestReused   = errors.New("occupancy request id was already used for a different lease")
	ErrOccupancyWorktreeUnknown = errors.New("occupancy worktree is not in the current snapshot")
)

// retainedReleasedLeaseLimit bounds released-lease history. Held leases are
// never pruned: only an explicit release ends one.
const retainedReleasedLeaseLimit = 500

type NewOccupancyLease struct {
	ID          string
	RequestID   string
	WorktreeID  string
	HolderKind  string
	HolderLabel string
}

// AcquireOccupancy records a held lease for a worktree that exists in the
// current snapshot, publishes it on that worktree in the same transaction,
// and appends the audit event. Repeating a request ID returns the existing
// lease without creating another one.
func (store *Store) AcquireOccupancy(ctx context.Context, request NewOccupancyLease) (contractv2.OccupancyLease, bool, error) {
	if request.ID == "" || request.RequestID == "" {
		return contractv2.OccupancyLease{}, false, errors.New("occupancy lease id and request id are required")
	}
	candidate := contractv2.OccupancyLease{
		ID: request.ID, WorktreeID: request.WorktreeID, HolderKind: request.HolderKind,
		HolderLabel: request.HolderLabel, State: "held", AcquiredAt: store.now().UTC(),
	}
	if err := candidate.Validate(); err != nil {
		return contractv2.OccupancyLease{}, false, err
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv2.OccupancyLease{}, false, fmt.Errorf("begin occupancy acquisition: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	existing, err := scanOccupancyLease(transaction.QueryRowContext(ctx, occupancyQuery+" WHERE request_id = ?", request.RequestID))
	if err == nil {
		if existing.WorktreeID != request.WorktreeID || existing.HolderKind != request.HolderKind ||
			existing.HolderLabel != request.HolderLabel {
			return contractv2.OccupancyLease{}, false, ErrOccupancyRequestReused
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return contractv2.OccupancyLease{}, false, fmt.Errorf("read idempotent occupancy lease: %w", err)
	}

	snapshot, err := readSnapshotTransaction(ctx, transaction)
	if err != nil {
		return contractv2.OccupancyLease{}, false, fmt.Errorf("read snapshot for occupancy: %w", err)
	}
	if !snapshotContainsWorktree(snapshot, request.WorktreeID) {
		return contractv2.OccupancyLease{}, false, ErrOccupancyWorktreeUnknown
	}
	var held int
	if err := transaction.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM occupancy_leases WHERE worktree_id = ? AND state = 'held'", request.WorktreeID,
	).Scan(&held); err != nil {
		return contractv2.OccupancyLease{}, false, fmt.Errorf("count held occupancy leases: %w", err)
	}
	if held >= contractv2.MaximumHeldOccupancyLeases {
		return contractv2.OccupancyLease{}, false, ErrOccupancyLimit
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO occupancy_leases(id, request_id, worktree_id, holder_kind, holder_label, state, acquired_at, released_at)
VALUES (?, ?, ?, ?, ?, 'held', ?, NULL)`,
		candidate.ID, request.RequestID, candidate.WorktreeID, candidate.HolderKind, candidate.HolderLabel,
		candidate.AcquiredAt.Format(timeFormat),
	); err != nil {
		return contractv2.OccupancyLease{}, false, fmt.Errorf("persist occupancy lease: %w", err)
	}
	if err := store.commitOccupancySnapshot(ctx, transaction, snapshot); err != nil {
		return contractv2.OccupancyLease{}, false, err
	}
	if err := store.recordAuditEvent(ctx, transaction, events.KindOccupancyAcquired, "", events.OccupancyAuditPayload{
		LeaseID: candidate.ID, WorktreeID: candidate.WorktreeID, HolderKind: candidate.HolderKind,
	}); err != nil {
		return contractv2.OccupancyLease{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv2.OccupancyLease{}, false, fmt.Errorf("commit occupancy acquisition: %w", err)
	}
	return candidate, true, nil
}

// ReleaseOccupancy ends a held lease, removes it from the published worktree
// in the same transaction, prunes released history, and appends the audit
// event. Releasing an already released lease returns it unchanged.
func (store *Store) ReleaseOccupancy(ctx context.Context, worktreeID, leaseID string) (contractv2.OccupancyLease, error) {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return contractv2.OccupancyLease{}, fmt.Errorf("begin occupancy release: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	lease, err := scanOccupancyLease(transaction.QueryRowContext(ctx, occupancyQuery+" WHERE id = ?", leaseID))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && lease.WorktreeID != worktreeID) {
		return contractv2.OccupancyLease{}, ErrOccupancyLeaseNotFound
	}
	if err != nil {
		return contractv2.OccupancyLease{}, fmt.Errorf("read occupancy lease: %w", err)
	}
	if lease.State == "released" {
		return lease, nil
	}
	releasedAt := store.now().UTC()
	if releasedAt.Before(lease.AcquiredAt) {
		releasedAt = lease.AcquiredAt
	}
	if _, err := transaction.ExecContext(ctx,
		"UPDATE occupancy_leases SET state = 'released', released_at = ? WHERE id = ?",
		releasedAt.Format(timeFormat), leaseID,
	); err != nil {
		return contractv2.OccupancyLease{}, fmt.Errorf("release occupancy lease: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM occupancy_leases
WHERE state = 'released' AND id NOT IN (
    SELECT id FROM occupancy_leases WHERE state = 'released'
    ORDER BY released_at DESC, id DESC LIMIT ?
)`, retainedReleasedLeaseLimit); err != nil {
		return contractv2.OccupancyLease{}, fmt.Errorf("prune released occupancy leases: %w", err)
	}
	snapshot, err := readSnapshotTransaction(ctx, transaction)
	if err != nil && !errors.Is(err, ErrNoSnapshot) {
		return contractv2.OccupancyLease{}, fmt.Errorf("read snapshot for occupancy release: %w", err)
	}
	if err == nil {
		if err := store.commitOccupancySnapshot(ctx, transaction, snapshot); err != nil {
			return contractv2.OccupancyLease{}, err
		}
	}
	lease.State = "released"
	lease.ReleasedAt = &releasedAt
	if err := store.recordAuditEvent(ctx, transaction, events.KindOccupancyReleased, "", events.OccupancyAuditPayload{
		LeaseID: lease.ID, WorktreeID: lease.WorktreeID, HolderKind: lease.HolderKind,
	}); err != nil {
		return contractv2.OccupancyLease{}, err
	}
	if err := transaction.Commit(); err != nil {
		return contractv2.OccupancyLease{}, fmt.Errorf("commit occupancy release: %w", err)
	}
	return lease, nil
}

// ListHeldOccupancy returns every held lease ordered by acquisition.
func (store *Store) ListHeldOccupancy(ctx context.Context) ([]contractv2.OccupancyLease, error) {
	return listHeldOccupancy(ctx, store.database)
}

// HeldOccupancyForWorktree returns the held leases of one worktree.
func (store *Store) HeldOccupancyForWorktree(ctx context.Context, worktreeID string) ([]contractv2.OccupancyLease, error) {
	held, err := listHeldOccupancy(ctx, store.database)
	if err != nil {
		return nil, err
	}
	filtered := make([]contractv2.OccupancyLease, 0)
	for _, lease := range held {
		if lease.WorktreeID == worktreeID {
			filtered = append(filtered, lease)
		}
	}
	return filtered, nil
}

// ProjectOccupancy publishes held leases on their worktrees and clears
// occupancy from every other worktree. Callers that rebuild worktree inventory
// use it to re-attach durable leases to freshly discovered worktrees.
func ProjectOccupancy(snapshot *contractv2.StatusSnapshot, held []contractv2.OccupancyLease) {
	byWorktree := make(map[string][]contractv2.OccupancyLease)
	for _, lease := range held {
		byWorktree[lease.WorktreeID] = append(byWorktree[lease.WorktreeID], lease)
	}
	for repositoryIndex := range snapshot.Repositories {
		for worktreeIndex := range snapshot.Repositories[repositoryIndex].Worktrees {
			worktree := &snapshot.Repositories[repositoryIndex].Worktrees[worktreeIndex]
			worktree.Occupancy = byWorktree[worktree.ID]
		}
	}
}

func (store *Store) commitOccupancySnapshot(ctx context.Context, transaction *sql.Tx, snapshot contractv2.StatusSnapshot) error {
	held, err := listHeldOccupancy(ctx, transaction)
	if err != nil {
		return err
	}
	ProjectOccupancy(&snapshot, held)
	if _, err := store.commitSnapshotTransaction(ctx, transaction, snapshot); err != nil {
		return fmt.Errorf("commit occupancy status snapshot: %w", err)
	}
	return nil
}

func snapshotContainsWorktree(snapshot contractv2.StatusSnapshot, worktreeID string) bool {
	for _, repository := range snapshot.Repositories {
		for _, worktree := range repository.Worktrees {
			if worktree.ID == worktreeID {
				return true
			}
		}
	}
	return false
}

const occupancyQuery = `
SELECT id, worktree_id, holder_kind, holder_label, state, acquired_at, released_at
FROM occupancy_leases`

func listHeldOccupancy(ctx context.Context, queryer operationQueryer) ([]contractv2.OccupancyLease, error) {
	rows, err := queryer.QueryContext(ctx, occupancyQuery+" WHERE state = 'held' ORDER BY acquired_at ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("list held occupancy leases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	leases := make([]contractv2.OccupancyLease, 0)
	for rows.Next() {
		lease, err := scanOccupancyLease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan occupancy lease: %w", err)
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate occupancy leases: %w", err)
	}
	return leases, nil
}

func scanOccupancyLease(row rowScanner) (contractv2.OccupancyLease, error) {
	var lease contractv2.OccupancyLease
	var acquiredAt string
	var releasedAt sql.NullString
	if err := row.Scan(&lease.ID, &lease.WorktreeID, &lease.HolderKind, &lease.HolderLabel, &lease.State, &acquiredAt, &releasedAt); err != nil {
		return contractv2.OccupancyLease{}, err
	}
	parsedAcquiredAt, err := time.Parse(timeFormat, acquiredAt)
	if err != nil {
		return contractv2.OccupancyLease{}, fmt.Errorf("parse occupancy acquisition time: %w", err)
	}
	lease.AcquiredAt = parsedAcquiredAt
	if releasedAt.Valid {
		parsedReleasedAt, err := time.Parse(timeFormat, releasedAt.String)
		if err != nil {
			return contractv2.OccupancyLease{}, fmt.Errorf("parse occupancy release time: %w", err)
		}
		lease.ReleasedAt = &parsedReleasedAt
	}
	if err := lease.Validate(); err != nil {
		return contractv2.OccupancyLease{}, fmt.Errorf("stored occupancy lease is invalid: %w", err)
	}
	return lease, nil
}

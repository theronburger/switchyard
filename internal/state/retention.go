package state

import (
	"context"
	"database/sql"
	"fmt"
)

// retainedTerminalOperationLimit bounds how many succeeded, failed, or
// cancelled operations remain in the ledger and therefore in every status
// snapshot. Incomplete operations and operations still referenced by a current
// environment or workspace result are never pruned.
const retainedTerminalOperationLimit = 500

// retainedConfigurationRevisionLimit bounds unreferenced accepted
// configuration revisions. The head revision and every revision whose
// repository digest is pinned by a live environment result or an incomplete
// environment operation are always retained, so a pinned payload can be
// recovered after any number of later acceptances. A stopped result is
// finished history and pins nothing.
const retainedConfigurationRevisionLimit = 16

// retainedConfigurationCandidateLimit bounds staged-but-unaccepted
// candidates, each of which holds a complete canonical payload. Editing and
// re-validating repeatedly without accepting must not grow the database
// without bound.
const retainedConfigurationCandidateLimit = 16

// pruneTerminalOperations removes the oldest terminal operations beyond the
// retention limit together with their private journal rows. It must run inside
// the transaction that changes the ledger so the bound is enforced
// transactionally.
func pruneTerminalOperations(ctx context.Context, transaction *sql.Tx) error {
	const prunable = `
SELECT id FROM operations
WHERE state IN ('succeeded', 'failed', 'cancelled')
  AND id NOT IN (SELECT operation_id FROM environment_current_results)
  AND id NOT IN (SELECT operation_id FROM workspace_current_results)
  AND id NOT IN (
      SELECT id FROM operations
      WHERE state IN ('succeeded', 'failed', 'cancelled')
      ORDER BY julianday(updated_at) DESC, rowid DESC
      LIMIT ?)`
	for _, statement := range []string{
		"DELETE FROM environment_operation_records WHERE operation_id IN (" + prunable + ")",
		"DELETE FROM workspace_operation_records WHERE operation_id IN (" + prunable + ")",
		"DELETE FROM operations WHERE id IN (" + prunable + ")",
	} {
		if _, err := transaction.ExecContext(ctx, statement, retainedTerminalOperationLimit); err != nil {
			return fmt.Errorf("prune terminal operations: %w", err)
		}
	}
	return nil
}

// pruneConfigurationRevisions removes accepted revisions that are neither the
// head, among the most recent retained revisions, nor pinned by a live
// environment resource. Timestamps are compared with julianday because the
// RFC 3339 text has a variable-width fraction and does not sort
// chronologically as text.
func pruneConfigurationRevisions(ctx context.Context, transaction *sql.Tx) error {
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM configuration_revisions
WHERE revision NOT IN (SELECT revision FROM configuration_head WHERE singleton = 1)
  AND revision NOT IN (
      SELECT revision FROM configuration_revisions ORDER BY revision DESC LIMIT ?)
  AND NOT EXISTS (
      SELECT 1
      FROM json_each(configuration_revisions.repository_digests_json) AS pinned
      WHERE pinned.value IN (
          SELECT json_extract(result_json, '$.ProfileDigest') FROM environment_current_results
          WHERE json_extract(result_json, '$.State') <> 'stopped'
          UNION
          SELECT json_extract(record_json, '$.Intent.ProfileDigest')
          FROM environment_operation_records
          WHERE operation_state IN ('pending', 'running')))`,
		retainedConfigurationRevisionLimit); err != nil {
		return fmt.Errorf("prune configuration revisions: %w", err)
	}
	return nil
}

// pruneConfigurationCandidates keeps only the most recently staged candidates.
func pruneConfigurationCandidates(ctx context.Context, transaction *sql.Tx) error {
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM configuration_candidates
WHERE rowid NOT IN (SELECT rowid FROM configuration_candidates ORDER BY rowid DESC LIMIT ?)`,
		retainedConfigurationCandidateLimit); err != nil {
		return fmt.Errorf("prune configuration candidates: %w", err)
	}
	return nil
}

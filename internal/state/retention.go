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
// repository digest is pinned by a current environment result or an incomplete
// environment operation are always retained, so a pinned payload can be
// recovered after any number of later acceptances.
const retainedConfigurationRevisionLimit = 16

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
      ORDER BY updated_at DESC, id DESC
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
// head, among the most recent retained revisions, nor pinned by a durable
// environment resource.
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
          UNION
          SELECT json_extract(record_json, '$.Intent.ProfileDigest')
          FROM environment_operation_records
          WHERE operation_state IN ('pending', 'running')))`,
		retainedConfigurationRevisionLimit); err != nil {
		return fmt.Errorf("prune configuration revisions: %w", err)
	}
	return nil
}

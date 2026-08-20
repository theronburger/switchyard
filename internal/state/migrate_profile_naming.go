package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// legacyRecordSchemaVersion is the record schema written by Switchyard 0.1.0
// for environment and workspace journal rows.
const legacyRecordSchemaVersion = 1

// migrateLegacyProfileNaming rewrites every persisted payload that used the
// pre-0.2.0 adapter naming. It preserves unknown keys and numeric literals so
// the rewritten payloads still satisfy the strict decoders, and it refuses to
// continue when a legacy payload is malformed rather than guessing.
func migrateLegacyProfileNaming(ctx context.Context, transaction *sql.Tx) error {
	if err := migrateLegacySnapshot(ctx, transaction); err != nil {
		return err
	}
	if err := rewriteLegacyRows(ctx, transaction, "environment_operation_records", "operation_id", "record_json", legacyEnvironmentRecord); err != nil {
		return err
	}
	if err := rewriteLegacyRows(ctx, transaction, "environment_current_results", "environment_id", "result_json", nil); err != nil {
		return err
	}
	if err := rewriteLegacyRows(ctx, transaction, "workspace_operation_records", "operation_id", "record_json", nil); err != nil {
		return err
	}
	return rewriteLegacyRows(ctx, transaction, "workspace_current_results", "worktree_id", "result_json", legacyWorkspaceResult)
}

func migrateLegacySnapshot(ctx context.Context, transaction *sql.Tx) error {
	var payload []byte
	err := transaction.QueryRowContext(ctx, "SELECT payload_json FROM current_snapshot WHERE singleton = 1").Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy status snapshot: %w", err)
	}
	document, err := decodeLegacyObject(payload)
	if err != nil {
		return fmt.Errorf("decode legacy status snapshot: %w", err)
	}
	version, ok := document["schemaVersion"].(json.Number)
	switch {
	case ok && version.String() == "2":
		// Already migrated; the migration is idempotent.
		return nil
	case ok && version.String() == "1":
	default:
		return fmt.Errorf("legacy status snapshot schema version %v is not 1", document["schemaVersion"])
	}
	document["schemaVersion"] = json.Number("2")
	repositories, ok := document["repositories"].([]any)
	if !ok {
		return errors.New("legacy status snapshot repositories is not an array")
	}
	for _, entry := range repositories {
		repository, ok := entry.(map[string]any)
		if !ok {
			return errors.New("legacy status snapshot repository is not an object")
		}
		if err := renameKey(repository, "adapter", "profileKey"); err != nil {
			return fmt.Errorf("legacy status snapshot repository: %w", err)
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode migrated status snapshot: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE current_snapshot SET payload_json = ? WHERE singleton = 1", encoded); err != nil {
		return fmt.Errorf("rewrite status snapshot: %w", err)
	}
	return nil
}

type legacyRewrite func(map[string]any) error

func legacyEnvironmentRecord(record map[string]any) error {
	intent, present := record["Intent"]
	if !present || intent == nil {
		return nil
	}
	object, ok := intent.(map[string]any)
	if !ok {
		return errors.New("legacy environment intent is not an object")
	}
	return renameKey(object, "Adapter", "ProfileDigest")
}

func legacyWorkspaceResult(result map[string]any) error {
	return renameKey(result, "Adapter", "ProfileKey")
}

func rewriteLegacyRows(ctx context.Context, transaction *sql.Tx, table, keyColumn, payloadColumn string, rewrite legacyRewrite) error {
	rows, err := transaction.QueryContext(ctx, fmt.Sprintf(
		"SELECT %s, %s FROM %s WHERE schema_version = ?", keyColumn, payloadColumn, table), legacyRecordSchemaVersion)
	if err != nil {
		return fmt.Errorf("read legacy %s: %w", table, err)
	}
	type pending struct {
		key     string
		payload []byte
	}
	updates := make([]pending, 0)
	for rows.Next() {
		var key string
		var payload []byte
		if err := rows.Scan(&key, &payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy %s: %w", table, err)
		}
		// Every legacy row is decoded before it is stamped with the new
		// schema version, even when its shape did not change: a payload that
		// is not one well-formed JSON object is refused, never relabelled.
		document, err := decodeLegacyObject(payload)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode legacy %s %q: %w", table, key, err)
		}
		if rewrite != nil {
			if err := rewrite(document); err != nil {
				_ = rows.Close()
				return fmt.Errorf("migrate legacy %s %q: %w", table, key, err)
			}
			if payload, err = json.Marshal(document); err != nil {
				_ = rows.Close()
				return fmt.Errorf("encode migrated %s %q: %w", table, key, err)
			}
		}
		updates = append(updates, pending{key: key, payload: payload})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate legacy %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy %s: %w", table, err)
	}
	for _, update := range updates {
		if _, err := transaction.ExecContext(ctx, fmt.Sprintf(
			"UPDATE %s SET schema_version = ?, %s = ? WHERE %s = ?", table, payloadColumn, keyColumn),
			legacyRecordSchemaVersion+1, update.payload, update.key); err != nil {
			return fmt.Errorf("rewrite legacy %s %q: %w", table, update.key, err)
		}
	}
	return nil
}

func decodeLegacyObject(payload []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("payload is not a JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("payload has trailing data")
	}
	return document, nil
}

func renameKey(object map[string]any, from, to string) error {
	value, present := object[from]
	if !present {
		if _, already := object[to]; already {
			return nil
		}
		return fmt.Errorf("missing %q", from)
	}
	if _, conflict := object[to]; conflict {
		return fmt.Errorf("both %q and %q are present", from, to)
	}
	if text, ok := value.(string); !ok || text == "" {
		return fmt.Errorf("%q is not a non-empty string", from)
	}
	delete(object, from)
	object[to] = value
	return nil
}

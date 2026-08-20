package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/theronburger/switchyard/internal/events"
)

const retainedEventLimit = 10_000

func (store *Store) AppendEvent(ctx context.Context, newEvent events.NewEvent) (events.Event, error) {
	if newEvent.ID == "" {
		return events.Event{}, errors.New("event id is required")
	}
	if newEvent.Kind == "" {
		return events.Event{}, errors.New("event kind is required")
	}
	if newEvent.Revision < 0 {
		return events.Event{}, errors.New("event revision must not be negative")
	}
	if len(newEvent.Payload) == 0 {
		newEvent.Payload = json.RawMessage("{}")
	}
	if !json.Valid(newEvent.Payload) {
		return events.Event{}, errors.New("event payload must be valid JSON")
	}
	if newEvent.OccurredAt.IsZero() {
		newEvent.OccurredAt = store.now().UTC()
	} else {
		newEvent.OccurredAt = newEvent.OccurredAt.UTC()
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return events.Event{}, fmt.Errorf("begin event append: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	result, err := transaction.ExecContext(ctx, `
INSERT INTO events(id, snapshot_revision, kind, environment_id, occurred_at, payload_json)
VALUES (?, ?, ?, NULLIF(?, ''), ?, ?)`,
		newEvent.ID,
		newEvent.Revision,
		newEvent.Kind,
		newEvent.EnvironmentID,
		newEvent.OccurredAt.Format(timeFormat),
		[]byte(newEvent.Payload),
	)
	if err != nil {
		return events.Event{}, fmt.Errorf("append event: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
DELETE FROM events
WHERE sequence <= COALESCE((SELECT MAX(sequence) - ? FROM events), 0)`, retainedEventLimit); err != nil {
		return events.Event{}, fmt.Errorf("prune event history: %w", err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return events.Event{}, fmt.Errorf("read event cursor: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return events.Event{}, fmt.Errorf("commit event append: %w", err)
	}
	return events.Event{
		Cursor:        events.Cursor(sequence),
		ID:            newEvent.ID,
		Revision:      newEvent.Revision,
		Kind:          newEvent.Kind,
		EnvironmentID: newEvent.EnvironmentID,
		OccurredAt:    newEvent.OccurredAt,
		Payload:       append(json.RawMessage(nil), newEvent.Payload...),
	}, nil
}

func (store *Store) ReadEvents(ctx context.Context, after events.Cursor, requestedLimit int) (events.Page, error) {
	if after < 0 {
		return events.Page{}, events.ErrInvalidCursor
	}
	limit := events.NormalizePageSize(requestedLimit)
	rows, err := store.database.QueryContext(ctx, `
SELECT sequence, id, snapshot_revision, kind, environment_id, occurred_at, payload_json
FROM events
WHERE sequence > ?
ORDER BY sequence
LIMIT ?`, int64(after), limit+1)
	if err != nil {
		return events.Page{}, fmt.Errorf("read events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	page := events.Page{Events: make([]events.Event, 0, limit), NextCursor: after}
	for rows.Next() {
		var event events.Event
		var environmentID sql.NullString
		var occurredAt string
		var payload []byte
		if err := rows.Scan(
			&event.Cursor,
			&event.ID,
			&event.Revision,
			&event.Kind,
			&environmentID,
			&occurredAt,
			&payload,
		); err != nil {
			return events.Page{}, fmt.Errorf("scan event: %w", err)
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		parsedOccurredAt, err := time.Parse(timeFormat, occurredAt)
		if err != nil {
			return events.Page{}, fmt.Errorf("parse event time: %w", err)
		}
		event.EnvironmentID = environmentID.String
		event.OccurredAt = parsedOccurredAt
		event.Payload = json.RawMessage(payload)
		page.Events = append(page.Events, event)
		page.NextCursor = event.Cursor
	}
	if err := rows.Err(); err != nil {
		return events.Page{}, fmt.Errorf("iterate events: %w", err)
	}
	return page, nil
}

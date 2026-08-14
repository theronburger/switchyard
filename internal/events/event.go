package events

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

const (
	DefaultPageSize = 100
	MaximumPageSize = 1000
)

var ErrInvalidCursor = errors.New("invalid event cursor")

type Cursor int64

func ParseCursor(value string) (Cursor, error) {
	if value == "" {
		return 0, nil
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, ErrInvalidCursor
	}
	return Cursor(parsed), nil
}

func (cursor Cursor) String() string {
	return strconv.FormatInt(int64(cursor), 10)
}

type NewEvent struct {
	ID            string
	Revision      int64
	Kind          string
	EnvironmentID string
	OccurredAt    time.Time
	Payload       json.RawMessage
}

type Event struct {
	Cursor        Cursor          `json:"cursor"`
	ID            string          `json:"id"`
	Revision      int64           `json:"revision"`
	Kind          string          `json:"kind"`
	EnvironmentID string          `json:"environmentId,omitempty"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Payload       json.RawMessage `json:"payload"`
}

type Page struct {
	Events     []Event `json:"events"`
	NextCursor Cursor  `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
}

func NormalizePageSize(requested int) int {
	if requested <= 0 {
		return DefaultPageSize
	}
	if requested > MaximumPageSize {
		return MaximumPageSize
	}
	return requested
}

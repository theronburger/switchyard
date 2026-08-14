package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/theronburger/switchyard/internal/events"
)

func (handler *apiHandler) events(response http.ResponseWriter, request *http.Request) {
	if handler.eventSource == nil {
		writeError(response, http.StatusServiceUnavailable, "EVENTS_UNAVAILABLE", "Event history is not ready", true)
		return
	}
	after, limit, parseError := parseEventQuery(request.URL.RawQuery)
	if parseError != nil {
		writeError(response, http.StatusBadRequest, parseError.code, parseError.message, false)
		return
	}
	page, err := handler.eventSource.ReadEvents(request.Context(), after, limit)
	if err != nil {
		switch {
		case errors.Is(request.Context().Err(), context.Canceled):
			writeError(response, http.StatusRequestTimeout, "REQUEST_CANCELLED", "Request was cancelled", true)
		case errors.Is(request.Context().Err(), context.DeadlineExceeded):
			writeError(response, http.StatusGatewayTimeout, "REQUEST_TIMEOUT", "Request timed out", true)
		default:
			writeError(response, http.StatusServiceUnavailable, "EVENTS_UNAVAILABLE", "Event history is not ready", true)
		}
		return
	}
	if err := validateEventPage(page, after, limit); err != nil {
		writeError(response, http.StatusInternalServerError, "INVALID_EVENTS", "Stored event history is invalid", false)
		return
	}
	if page.Events == nil {
		page.Events = make([]events.Event, 0)
	}
	writeJSON(response, http.StatusOK, page)
}

type eventQueryError struct {
	code    string
	message string
}

func parseEventQuery(rawQuery string) (events.Cursor, int, *eventQueryError) {
	if strings.Contains(rawQuery, "%") || strings.HasPrefix(rawQuery, "&") || strings.HasSuffix(rawQuery, "&") || strings.Contains(rawQuery, "&&") {
		return 0, 0, invalidEventQuery()
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, 0, invalidEventQuery()
	}
	for key := range query {
		if key != "after" && key != "limit" {
			return 0, 0, invalidEventQuery()
		}
	}

	after := events.Cursor(0)
	if values, exists := query["after"]; exists {
		if len(values) != 1 || values[0] == "" {
			return 0, 0, &eventQueryError{code: "INVALID_EVENT_CURSOR", message: "Event cursor is invalid"}
		}
		parsed, parseErr := events.ParseCursor(values[0])
		if parseErr != nil || parsed.String() != values[0] {
			return 0, 0, &eventQueryError{code: "INVALID_EVENT_CURSOR", message: "Event cursor is invalid"}
		}
		after = parsed
	}

	limit := events.DefaultPageSize
	if values, exists := query["limit"]; exists {
		if len(values) != 1 || values[0] == "" {
			return 0, 0, &eventQueryError{code: "INVALID_EVENT_LIMIT", message: "Event limit is invalid"}
		}
		parsed, parseErr := strconv.ParseInt(values[0], 10, 32)
		if parseErr != nil || parsed < 1 || parsed > events.MaximumPageSize || strconv.FormatInt(parsed, 10) != values[0] {
			return 0, 0, &eventQueryError{code: "INVALID_EVENT_LIMIT", message: "Event limit is invalid"}
		}
		limit = int(parsed)
	}
	return after, limit, nil
}

func invalidEventQuery() *eventQueryError {
	return &eventQueryError{code: "INVALID_EVENT_QUERY", message: "Event query is invalid"}
}

func validateEventPage(page events.Page, after events.Cursor, limit int) error {
	if len(page.Events) > limit || page.NextCursor < after || (page.HasMore && len(page.Events) != limit) {
		return errors.New("invalid event page bounds")
	}
	previous := after
	for _, event := range page.Events {
		if event.Cursor <= previous || event.ID == "" || event.Kind == "" || event.Revision < 0 || event.OccurredAt.IsZero() || !json.Valid(event.Payload) {
			return errors.New("invalid event page entry")
		}
		previous = event.Cursor
	}
	if len(page.Events) == 0 {
		if page.NextCursor != after || page.HasMore {
			return errors.New("invalid empty event page")
		}
		return nil
	}
	if page.NextCursor != previous {
		return errors.New("invalid next event cursor")
	}
	return nil
}

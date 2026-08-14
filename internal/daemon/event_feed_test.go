package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/events"
)

type eventSourceFunc func(ctx context.Context, after events.Cursor, requestedLimit int) (events.Page, error)

func (function eventSourceFunc) ReadEvents(ctx context.Context, after events.Cursor, requestedLimit int) (events.Page, error) {
	return function(ctx, after, requestedLimit)
}

func TestEventsReturnsAuthenticatedResumablePage(t *testing.T) {
	t.Parallel()
	occurredAt := time.Date(2026, 8, 14, 10, 30, 0, 0, time.UTC)
	var gotAfter events.Cursor
	var gotLimit int
	source := eventSourceFunc(func(_ context.Context, after events.Cursor, limit int) (events.Page, error) {
		gotAfter = after
		gotLimit = limit
		return events.Page{
			Events: []events.Event{
				{Cursor: 42, ID: "event_42", Revision: 7, Kind: "snapshotCommitted", OccurredAt: occurredAt, Payload: json.RawMessage(`{"revision":7}`)},
				{Cursor: 43, ID: "event_43", Revision: 8, Kind: "healthChanged", EnvironmentID: "env_01", OccurredAt: occurredAt, Payload: json.RawMessage(`{"health":"healthy"}`)},
			},
			NextCursor: 43,
			HasMore:    true,
		}, nil
	})
	handler := newEventTestHandler(t, source)
	request := authenticatedRequest(http.MethodGet, "/v1/events?after=41&limit=2")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status: got %d, body %s", response.Code, response.Body.String())
	}
	if gotAfter != 41 || gotLimit != 2 {
		t.Fatalf("read arguments: after=%s limit=%d", gotAfter, gotLimit)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing security headers: %v", response.Header())
	}
	var page events.Page
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Cursor != 42 || page.NextCursor != 43 || !page.HasMore {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestEventsAlwaysReturnsNonNullEventArray(t *testing.T) {
	t.Parallel()
	handler := newEventTestHandler(t, eventSourceFunc(func(_ context.Context, after events.Cursor, _ int) (events.Page, error) {
		return events.Page{NextCursor: after}, nil
	}))
	request := authenticatedRequest(http.MethodGet, "/v1/events?after=9")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status: %d, body %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"events":[]`) || strings.Contains(response.Body.String(), `"events":null`) {
		t.Fatalf("event array was nullable: %s", response.Body.String())
	}
}

func TestEventsUsesBoundedDefaultLimit(t *testing.T) {
	t.Parallel()
	var gotLimit int
	handler := newEventTestHandler(t, eventSourceFunc(func(_ context.Context, after events.Cursor, limit int) (events.Page, error) {
		gotLimit = limit
		return events.Page{Events: []events.Event{}, NextCursor: after}, nil
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/events"))
	if response.Code != http.StatusOK || gotLimit != events.DefaultPageSize {
		t.Fatalf("status=%d limit=%d body=%s", response.Code, gotLimit, response.Body.String())
	}
}

func TestEventsRejectsNonCanonicalCursorAndLimitsBeforeStorage(t *testing.T) {
	t.Parallel()
	invalidQueries := []struct {
		query string
		code  string
	}{
		{"after=", "INVALID_EVENT_CURSOR"},
		{"after=-1", "INVALID_EVENT_CURSOR"},
		{"after=01", "INVALID_EVENT_CURSOR"},
		{"after=+1", "INVALID_EVENT_CURSOR"},
		{"after=9223372036854775808", "INVALID_EVENT_CURSOR"},
		{"after=1&after=2", "INVALID_EVENT_CURSOR"},
		{"after=%31", "INVALID_EVENT_QUERY"},
		{"limit=", "INVALID_EVENT_LIMIT"},
		{"limit=0", "INVALID_EVENT_LIMIT"},
		{"limit=01", "INVALID_EVENT_LIMIT"},
		{"limit=1001", "INVALID_EVENT_LIMIT"},
		{"limit=1&limit=2", "INVALID_EVENT_LIMIT"},
		{"cursor=1", "INVALID_EVENT_QUERY"},
		{"after=1&&limit=2", "INVALID_EVENT_QUERY"},
		{"after=1&", "INVALID_EVENT_QUERY"},
	}
	for _, test := range invalidQueries {
		t.Run(test.query, func(t *testing.T) {
			calls := 0
			handler := newEventTestHandler(t, eventSourceFunc(func(context.Context, events.Cursor, int) (events.Page, error) {
				calls++
				return events.Page{}, nil
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/events?"+test.query))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if calls != 0 {
				t.Fatalf("invalid query reached storage %d times", calls)
			}
		})
	}
}

func TestEventsAcceptsMaximumCanonicalCursorAndLimit(t *testing.T) {
	t.Parallel()
	const maximumCursor = events.Cursor(1<<63 - 1)
	handler := newEventTestHandler(t, eventSourceFunc(func(_ context.Context, after events.Cursor, limit int) (events.Page, error) {
		if after != maximumCursor || limit != events.MaximumPageSize {
			t.Fatalf("after=%s limit=%d", after, limit)
		}
		return events.Page{Events: []events.Event{}, NextCursor: after}, nil
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/events?after=9223372036854775807&limit=1000"))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEventsRequiresAuthenticationRejectsOriginAndMethod(t *testing.T) {
	t.Parallel()
	var calls int
	handler := newEventTestHandler(t, eventSourceFunc(func(context.Context, events.Cursor, int) (events.Page, error) {
		calls++
		return events.Page{}, nil
	}))

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/v1/events", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", unauthenticated.Code)
	}

	originRequest := authenticatedRequest(http.MethodGet, "/v1/events")
	originRequest.Header.Set("Origin", "null")
	originResponse := httptest.NewRecorder()
	handler.ServeHTTP(originResponse, originRequest)
	if originResponse.Code != http.StatusForbidden {
		t.Fatalf("origin status: %d", originResponse.Code)
	}

	methodResponse := httptest.NewRecorder()
	handler.ServeHTTP(methodResponse, authenticatedRequest(http.MethodPost, "/v1/events"))
	if methodResponse.Code != http.StatusMethodNotAllowed || methodResponse.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method status=%d allow=%q", methodResponse.Code, methodResponse.Header().Get("Allow"))
	}
	if calls != 0 {
		t.Fatalf("rejected requests reached storage %d times", calls)
	}
}

func TestEventsRedactsStorageFailure(t *testing.T) {
	t.Parallel()
	const privateDetail = "private /Users/person/state.sqlite credential-secret"
	handler := newEventTestHandler(t, eventSourceFunc(func(context.Context, events.Cursor, int) (events.Page, error) {
		return events.Page{}, errors.New(privateDetail)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/events"))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"EVENTS_UNAVAILABLE"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "Users") || strings.Contains(response.Body.String(), "credential-secret") || strings.Contains(response.Body.String(), "sqlite") {
		t.Fatalf("storage failure leaked: %s", response.Body.String())
	}
}

func TestEventsCancellationReachesSourceAndCompletes(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var once sync.Once
	handler := newEventTestHandler(t, eventSourceFunc(func(ctx context.Context, _ events.Cursor, _ int) (events.Page, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return events.Page{}, ctx.Err()
	}))
	request := authenticatedRequest(http.MethodGet, "/v1/events")
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled event request did not complete")
	}
	if response.Code != http.StatusRequestTimeout || !strings.Contains(response.Body.String(), `"code":"REQUEST_CANCELLED"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEventsRejectsInvalidPageWithoutLeakingPayload(t *testing.T) {
	t.Parallel()
	const secret = "payload-secret"
	handler := newEventTestHandler(t, eventSourceFunc(func(_ context.Context, _ events.Cursor, _ int) (events.Page, error) {
		return events.Page{
			Events:     []events.Event{{Cursor: 1, ID: "event_1", Kind: "changed", OccurredAt: time.Now(), Payload: json.RawMessage(`{"secret":"` + secret + `"}`)}},
			NextCursor: 0,
		}, nil
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/events"))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INVALID_EVENTS"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatalf("invalid page payload leaked: %s", response.Body.String())
	}
}

type combinedHTTPSource struct {
	staticStatusSource
	eventSource EventSource
}

func (source combinedHTTPSource) ReadEvents(ctx context.Context, after events.Cursor, limit int) (events.Page, error) {
	return source.eventSource.ReadEvents(ctx, after, limit)
}

func TestEventsUsesEventCapabilityFromStatusStore(t *testing.T) {
	t.Parallel()
	combined := combinedHTTPSource{
		staticStatusSource: staticStatusSource{snapshot: validHTTPStatus()},
		eventSource: eventSourceFunc(func(_ context.Context, after events.Cursor, _ int) (events.Page, error) {
			return events.Page{Events: []events.Event{}, NextCursor: after}, nil
		}),
	}
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "0.1.0-dev",
		StartedAt: time.Now(), StatusSource: combined,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/events"))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func newEventTestHandler(t *testing.T, source EventSource) http.Handler {
	t.Helper()
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "0.1.0-dev",
		StartedAt: time.Now(), StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, EventSource: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

var _ StatusSource = combinedHTTPSource{}
var _ EventSource = combinedHTTPSource{}

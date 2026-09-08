package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/workspaces"
)

func TestClientWaitsForLargeWorktreeMutations(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"prune", "create"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(31 * time.Second):
					_, _ = w.Write([]byte(`{}`))
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			root := t.TempDir()
			directory := filepath.Join(root, "daemon")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			descriptor, _ := json.Marshal(Descriptor{Version: 3, Endpoint: server.URL})
			if err := os.WriteFile(filepath.Join(directory, "runtime.json"), descriptor, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "token"), []byte("test-token"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := (Client{Root: root}).Request(context.Background(), http.MethodPost, "/api/"+route, struct{}{}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalAPIBoundary(t *testing.T) {
	engine, err := workspaces.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Close(context.Background()) }()
	server := Server{Engine: engine, Token: "local-test-token"}
	for _, test := range []struct {
		name, auth, origin, version string
		status                      int
	}{
		{"unauthenticated", "", "", "3", 401}, {"browser origin", "Bearer local-test-token", "http://example.test", "3", 401},
		{"contract mismatch", "Bearer local-test-token", "", "2", 426}, {"valid", "Bearer local-test-token", "", "3", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/api/status", nil)
			request.Header.Set("Authorization", test.auth)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("X-Switchyard-Version", test.version)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("local state must not be cached")
			}
		})
	}
	for _, body := range []string{`{"schemaVersion":1,"repositories":[],"unknown":true}`, `{"schemaVersion":1,"repositories":[]} {}`, `{`, `null`} {
		request := httptest.NewRequest("POST", "/api/config", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer local-test-token")
		request.Header.Set("X-Switchyard-Version", "3")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("accepted invalid config %q: %d", body, response.Code)
		}
	}
}

func TestClientRejectsNonlocalDescriptorBeforeSendingToken(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "daemon")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://example.com", "http://localhost:123", "http://127.0.0.1:123@evil.test", "http://127.0.0.1:123/path", "http://127.0.0.1:123?next=remote"} {
		raw, _ := json.Marshal(Descriptor{Version: 3, Endpoint: endpoint})
		if err := os.WriteFile(filepath.Join(directory, "runtime.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		err := (Client{Root: root}).Request(context.Background(), http.MethodGet, "/api/status", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "Invalid local helper") {
			t.Fatalf("endpoint %q: %v", endpoint, err)
		}
	}
}

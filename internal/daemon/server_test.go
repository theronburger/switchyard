package daemon

import (
	"context"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLoopbackServerServesAndShutsDown(t *testing.T) {
	listener, err := ListenLoopback(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t, validHTTPStatus())
	server, err := NewLoopbackServer(listener, handler)
	if err != nil {
		t.Fatal(err)
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve() }()

	request, err := http.NewRequest(http.MethodGet, server.Endpoint()+"/handshake", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(contractv2.SchemaVersionHeader, "2")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := response.StatusCode, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
}

func TestLoopbackServerRejectsWildcardListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_, err = NewLoopbackServer(listener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("wildcard listener error: got %v", err)
	}
}

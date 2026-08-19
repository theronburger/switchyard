package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProberAcceptsLeasedTCPAndHTTP(t *testing.T) {
	t.Parallel()
	tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tcpListener.Close() }()
	accepted := make(chan struct{})
	go func() {
		connection, acceptErr := tcpListener.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
		close(accepted)
	}()

	prober := newTestProber(t, time.Second)
	tcpResult, err := prober.Check(context.Background(), ProbeSpec{
		ID: "database", Kind: ProbeKindTCP, Lease: leaseFor(t, tcpListener.Addr().String()), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tcpResult.Success || tcpResult.Code != ResultOK {
		t.Fatalf("unexpected TCP result: %+v", tcpResult)
	}
	<-accepted

	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/ready" {
			t.Errorf("unexpected request path %q", request.URL.Path)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer httpServer.Close()
	httpResult, err := prober.Check(context.Background(), httpProbe(t, httpServer, "/ready", time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !httpResult.Success || httpResult.Status != http.StatusNoContent {
		t.Fatalf("unexpected HTTP result: %+v", httpResult)
	}
}

func TestProberRejectsForeignHostsWithoutDialing(t *testing.T) {
	t.Parallel()
	var dials atomic.Int32
	prober, err := NewProber(ProberConfig{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("should not dial")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost", "127.0.0.2", "::1", "example.com", "127.0.0.1.example.com"} {
		t.Run(host, func(t *testing.T) {
			_, checkErr := prober.Check(context.Background(), ProbeSpec{
				ID: "foreign", Kind: ProbeKindTCP, Lease: Lease{Host: host, Port: 8080}, Timeout: time.Second,
			})
			if !errors.Is(checkErr, ErrInvalidProbe) {
				t.Fatalf("expected ErrInvalidProbe, got %v", checkErr)
			}
		})
	}
	if dials.Load() != 0 {
		t.Fatalf("foreign hosts caused %d dial attempts", dials.Load())
	}
}

func TestHTTPProbeRefusesRedirect(t *testing.T) {
	t.Parallel()
	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		destinationHits.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL+"/credential-target", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	result, err := newTestProber(t, time.Second).Check(
		context.Background(),
		httpProbe(t, redirector, "/ready", time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Code != ResultUnexpectedStatus || result.Status != http.StatusTemporaryRedirect {
		t.Fatalf("unexpected redirect result: %+v", result)
	}
	if destinationHits.Load() != 0 {
		t.Fatalf("redirect destination received %d requests", destinationHits.Load())
	}
}

func TestHTTPProbeTimeoutIsBounded(t *testing.T) {
	t.Parallel()
	port, cleanup := hangingHTTPPort(t)
	defer cleanup()
	prober := newTestProber(t, 100*time.Millisecond)
	startedAt := time.Now()
	result, err := prober.Check(context.Background(), ProbeSpec{
		ID: "slow", Kind: ProbeKindHTTP, Lease: Lease{Host: LoopbackHost, Port: port},
		Method: http.MethodGet, Path: "/ready", AcceptedStatuses: []StatusRange{{Minimum: 200, Maximum: 299}}, Timeout: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Code != ResultTimeout || result.Success {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("probe exceeded its bound: %s", elapsed)
	}
}

func TestHTTPProbeCancellationPropagates(t *testing.T) {
	t.Parallel()
	port, cleanup := hangingHTTPPort(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := newTestProber(t, time.Second).Check(ctx, ProbeSpec{
		ID: "cancel", Kind: ProbeKindHTTP, Lease: Lease{Host: LoopbackHost, Port: port},
		Method: http.MethodGet, Path: "/ready", AcceptedStatuses: []StatusRange{{Minimum: 200, Maximum: 299}}, Timeout: time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestHTTPProbeDoesNotLeakBodyURLOrValidationInput(t *testing.T) {
	t.Parallel()
	const secret = "body-secret-that-must-not-escape"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Credential", "header-secret-that-must-not-escape")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(secret))
	}))
	defer server.Close()

	result, err := newTestProber(t, time.Second).Check(context.Background(), httpProbe(t, server, "/ready", time.Second))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, "header-secret", server.URL, "/ready"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("probe result leaked %q: %s", forbidden, encoded)
		}
	}

	const credentialPath = "/ready?token=credential-secret"
	_, err = newTestProber(t, time.Second).Check(context.Background(), ProbeSpec{
		ID: "invalid", Kind: ProbeKindHTTP, Lease: leaseFor(t, strings.TrimPrefix(server.URL, "http://")),
		Method: http.MethodGet, Path: credentialPath, AcceptedStatuses: []StatusRange{{Minimum: 200, Maximum: 299}}, Timeout: time.Second,
	})
	if !errors.Is(err, ErrInvalidProbe) || strings.Contains(err.Error(), credentialPath) || strings.Contains(err.Error(), "credential-secret") {
		t.Fatalf("unsafe validation error: %v", err)
	}
}

func TestProberRejectsExcessiveTimeoutAndUnsafeHTTPFields(t *testing.T) {
	t.Parallel()
	prober := newTestProber(t, time.Second)
	base := ProbeSpec{
		ID: "ready", Kind: ProbeKindHTTP, Lease: Lease{Host: LoopbackHost, Port: 8080}, Method: http.MethodGet,
		Path: "/ready", AcceptedStatuses: []StatusRange{{Minimum: 200, Maximum: 299}}, Timeout: 2 * time.Second,
	}
	if _, err := prober.Check(context.Background(), base); !errors.Is(err, ErrInvalidProbe) {
		t.Fatalf("expected timeout rejection, got %v", err)
	}
	base.Timeout = time.Second
	base.Path = "//example.com/ready"
	if _, err := prober.Check(context.Background(), base); !errors.Is(err, ErrInvalidProbe) {
		t.Fatalf("expected path rejection, got %v", err)
	}
}

func newTestProber(t *testing.T, maximum time.Duration) *Prober {
	t.Helper()
	prober, err := NewProber(ProberConfig{MaxTimeout: maximum})
	if err != nil {
		t.Fatal(err)
	}
	return prober
}

func httpProbe(t *testing.T, server *httptest.Server, path string, timeout time.Duration) ProbeSpec {
	t.Helper()
	return ProbeSpec{
		ID: "ready", Kind: ProbeKindHTTP, Lease: leaseFor(t, strings.TrimPrefix(server.URL, "http://")),
		Method: http.MethodGet, Path: path, AcceptedStatuses: []StatusRange{{Minimum: 200, Maximum: 299}}, Timeout: timeout,
	}
}

func leaseFor(t *testing.T, address string) Lease {
	t.Helper()
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if host != LoopbackHost {
		t.Fatalf("test listener is not on literal loopback: %s", host)
	}
	return Lease{Host: host, Port: port}
}

func hangingHTTPPort(t *testing.T) (int, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	connections := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			connections <- connection
		}
		close(connections)
	}()
	lease := leaseFor(t, listener.Addr().String())
	cleanup := func() {
		_ = listener.Close()
		for connection := range connections {
			_ = connection.Close()
		}
	}
	return lease.Port, cleanup
}

func ExampleProbeResult() {
	result := ProbeResult{ProbeID: "organizer-ready", Kind: ProbeKindHTTP, Code: ResultOK, Success: true, Status: 204}
	fmt.Println(result.Code, result.Success)
	// Output: ok true
}

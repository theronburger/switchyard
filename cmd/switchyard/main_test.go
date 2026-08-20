package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/state"
)

func TestLocalPathsUsesApplicationSupportOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv(applicationSupportOverride, root)

	paths, err := localPaths()
	if err != nil {
		t.Fatalf("localPaths: %v", err)
	}
	wantDirectory := filepath.Join(root, "Switchyard Development", "daemon")
	if paths.root != filepath.Dir(wantDirectory) {
		t.Fatalf("root: got %q, want %q", paths.root, filepath.Dir(wantDirectory))
	}
	if paths.directory != wantDirectory {
		t.Fatalf("directory: got %q, want %q", paths.directory, wantDirectory)
	}
	if paths.database != filepath.Join(wantDirectory, "state-v2.sqlite") {
		t.Fatalf("database: got %q", paths.database)
	}
	if paths.configuration != filepath.Join(root, "Switchyard Development", "configuration.yaml") {
		t.Fatalf("configuration: got %q", paths.configuration)
	}
	if paths.runtimeDescriptor != filepath.Join(wantDirectory, "runtime.json") {
		t.Fatalf("runtime descriptor: got %q", paths.runtimeDescriptor)
	}
	if paths.token != filepath.Join(wantDirectory, "token") {
		t.Fatalf("token: got %q", paths.token)
	}
}

func TestApplicationDirectoryNameSeparatesBuildChannels(t *testing.T) {
	tests := []struct {
		channel string
		want    string
	}{
		{channel: "development", want: "Switchyard Development"},
		{channel: "release", want: "Switchyard"},
	}
	for _, test := range tests {
		got, err := applicationDirectoryName(test.channel)
		if err != nil {
			t.Fatalf("applicationDirectoryName(%q): %v", test.channel, err)
		}
		if got != test.want {
			t.Fatalf("applicationDirectoryName(%q): got %q, want %q", test.channel, got, test.want)
		}
	}
	if _, err := applicationDirectoryName("unknown"); err == nil {
		t.Fatal("unsupported build channel was accepted")
	}
}

func TestDaemonWiringServesAuthenticatedStatusAndShutsDownCleanly(t *testing.T) {
	root := t.TempDir()
	t.Setenv(gitExecutableOverride, "/usr/bin/false")
	paths := applicationPaths{
		directory:         root,
		database:          filepath.Join(root, "state.sqlite"),
		runtimeDescriptor: filepath.Join(root, "runtime.json"),
		token:             filepath.Join(root, "token"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- runDaemon(ctx, paths)
	}()
	t.Cleanup(cancel)

	connector := newConnector(paths)

	deadline := time.Now().Add(5 * time.Second)
	var daemonErr error
	for {
		select {
		case runErr := <-errCh:
			t.Fatalf("daemon exited before readiness: %v", runErr)
		default:
		}
		snapshot, err := connector.Status(context.Background())
		if err == nil {
			if snapshot.Daemon.State != "ready" || snapshot.Daemon.Version != version {
				t.Fatalf("unexpected daemon status: %+v", snapshot.Daemon)
			}
			if snapshot.Repositories == nil || snapshot.Environments == nil ||
				snapshot.Operations == nil || snapshot.Alerts == nil {
				t.Fatal("daemon status encoded a top-level collection as null")
			}
			break
		}
		daemonErr = err
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not become ready: %v", daemonErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, path := range []string{paths.runtimeDescriptor, paths.token} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("inspect %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode for %s: got %04o, want 0600", path, info.Mode().Perm())
		}
	}

	if report := (apiclient.Doctor{Connector: connector}).Run(context.Background()); !report.Healthy {
		t.Fatalf("doctor was unhealthy: %+v", report.Checks)
	}
	if err := runDaemon(context.Background(), paths); !errors.Is(err, state.ErrStoreLocked) {
		t.Fatalf("second daemon: got %v, want state store lock error", err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("daemon shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down")
	}
	if _, err := os.Lstat(paths.runtimeDescriptor); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime descriptor remains after shutdown: %v", err)
	}
	if _, err := os.Lstat(paths.token); err != nil {
		t.Fatalf("persistent daemon token was removed: %v", err)
	}
}

func TestDaemonDoesNotPublishRuntimeFilesWhenAlreadyCancelled(t *testing.T) {
	root := t.TempDir()
	paths := applicationPaths{
		directory:         root,
		database:          filepath.Join(root, "state.sqlite"),
		runtimeDescriptor: filepath.Join(root, "runtime.json"),
		token:             filepath.Join(root, "token"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runDaemon(ctx, paths); err != nil {
		t.Fatalf("cancelled daemon: %v", err)
	}
	for _, path := range []string{paths.database, paths.runtimeDescriptor, paths.token} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled daemon created %s: %v", path, err)
		}
	}
}

func TestConnectorRejectsReusedPortBeforeSendingAuthorization(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var requestCount atomic.Int32
	httpServer := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	})}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })

	root := t.TempDir()
	paths := applicationPaths{
		runtimeDescriptor: filepath.Join(root, "runtime.json"),
		token:             filepath.Join(root, "token"),
	}
	if _, err := daemon.LoadOrCreateToken(paths.token, bytes.NewReader(make([]byte, 32))); err != nil {
		t.Fatalf("create token: %v", err)
	}
	actualStartedAt, err := processStartedAt(os.Getpid())
	if err != nil {
		t.Fatalf("read process start: %v", err)
	}
	descriptor := contractv1.RuntimeDescriptor{
		SchemaVersion:    contractv1.SchemaVersion,
		Endpoint:         fmt.Sprintf("http://%s", listener.Addr()),
		DaemonInstanceID: "daemon_stale",
		DaemonVersion:    version,
		PID:              os.Getpid(),
		ProcessStartedAt: actualStartedAt.Add(-time.Second),
		GeneratedAt:      time.Now().UTC(),
	}
	if err := daemon.PublishRuntimeDescriptor(paths.runtimeDescriptor, descriptor); err != nil {
		t.Fatalf("publish stale descriptor: %v", err)
	}

	_, err = newConnector(paths).Status(context.Background())
	if apiclient.CodeOf(err) != apiclient.ErrorRuntimeDescriptorStale {
		t.Fatalf("status error: got %v (%s), want stale descriptor", err, apiclient.CodeOf(err))
	}
	if got := requestCount.Load(); got != 0 {
		t.Fatalf("unrelated listener received %d authenticated request(s)", got)
	}
}

func TestProcessIdentityMatchesCurrentProcess(t *testing.T) {
	startedAt, err := processStartedAt(os.Getpid())
	if err != nil {
		t.Fatalf("processStartedAt: %v", err)
	}
	if err := verifyProcessIdentity(os.Getpid(), startedAt); err != nil {
		t.Fatalf("verifyProcessIdentity: %v", err)
	}
}

func TestRemoveOwnedRuntimeDescriptorPreservesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	contents := []byte(`{"daemonInstanceId":"replacement"}`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	removeOwnedRuntimeDescriptor(path, "old-instance")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replacement descriptor removed: %v", err)
	}
	if string(got) != string(contents) {
		t.Fatalf("replacement descriptor changed: got %q", got)
	}
}

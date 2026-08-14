package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	"github.com/theronburger/switchyard/internal/state"
)

func TestLocalPathsUsesApplicationSupportOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv(applicationSupportOverride, root)

	paths, err := localPaths()
	if err != nil {
		t.Fatalf("localPaths: %v", err)
	}
	wantDirectory := filepath.Join(root, "Switchyard", "daemon")
	if paths.directory != wantDirectory {
		t.Fatalf("directory: got %q, want %q", paths.directory, wantDirectory)
	}
	if paths.database != filepath.Join(wantDirectory, "state.sqlite") {
		t.Fatalf("database: got %q", paths.database)
	}
	if paths.runtimeDescriptor != filepath.Join(wantDirectory, "runtime.json") {
		t.Fatalf("runtime descriptor: got %q", paths.runtimeDescriptor)
	}
	if paths.token != filepath.Join(wantDirectory, "token") {
		t.Fatalf("token: got %q", paths.token)
	}
}

func TestDaemonWiringServesAuthenticatedStatusAndShutsDownCleanly(t *testing.T) {
	root := t.TempDir()
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

	connector := apiclient.Connector{
		Paths: apiclient.RuntimePaths{
			Descriptor: paths.runtimeDescriptor,
			Token:      paths.token,
		},
		DiscoveryPolicy: apiclient.DiscoveryPolicy{RequiredDaemonVersion: version},
		ClientOptions:   apiclient.ClientOptions{RequiredDaemonVersion: version},
	}

	deadline := time.Now().Add(5 * time.Second)
	var daemonErr error
	for {
		snapshot, err := connector.Status(context.Background())
		if err == nil {
			if snapshot.Daemon.State != "ready" || snapshot.Daemon.Version != version {
				t.Fatalf("unexpected daemon status: %+v", snapshot.Daemon)
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

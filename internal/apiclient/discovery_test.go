package apiclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverLoadsSeparatePrivateRuntimeFiles(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         "http://127.0.0.1:43123",
		DaemonInstanceID: "daemon_test",
		DaemonVersion:    "0.1.0-dev",
		PID:              123,
		ProcessStartedAt: now.Add(-time.Hour),
		GeneratedAt:      now.Add(-time.Hour),
	}, testToken())

	connection, err := Discover(paths, DiscoveryPolicy{
		Now:                   func() time.Time { return now },
		RequiredDaemonVersion: "0.1.0-dev",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := connection.Descriptor().DaemonInstanceID; got != "daemon_test" {
		t.Fatalf("daemon instance: got %q", got)
	}
	encoded, err := json.Marshal(connection) //nolint:staticcheck // Empty JSON is intentional: the test proves private connection fields never serialize.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), testToken()) {
		t.Fatal("connection JSON exposed its bearer token")
	}
}

func TestDiscoverRejectsHostileEndpoints(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	endpoints := []string{
		"https://127.0.0.1:43123",
		"http://localhost:43123",
		"http://127.0.0.1.evil.example:43123",
		"http://127.0.0.1:43123/status",
		"http://user:secret@127.0.0.1:43123",
		"http://127.0.0.1:43123?token=secret",
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			paths := writeRuntimeFiles(t, RuntimeDescriptor{
				SchemaVersion:    RuntimeDescriptorSchemaVersion,
				Endpoint:         endpoint,
				DaemonInstanceID: "daemon_test",
				DaemonVersion:    "0.1.0-dev",
				PID:              123,
				ProcessStartedAt: now.Add(-time.Minute),
				GeneratedAt:      now,
			}, testToken())
			_, err := Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }})
			if CodeOf(err) != ErrorRuntimeEndpointUnsafe {
				t.Fatalf("error code: got %q, want %q", CodeOf(err), ErrorRuntimeEndpointUnsafe)
			}
		})
	}
}

func TestDiscoverRejectsUnsafePermissionsAndSymlinks(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	descriptor := RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         "http://127.0.0.1:43123",
		DaemonInstanceID: "daemon_test",
		DaemonVersion:    "0.1.0-dev",
		PID:              123,
		ProcessStartedAt: now.Add(-time.Minute),
		GeneratedAt:      now,
	}

	t.Run("permissions", func(t *testing.T) {
		paths := writeRuntimeFiles(t, descriptor, testToken())
		if err := os.Chmod(paths.Token, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }})
		if CodeOf(err) != ErrorRuntimeTokenUnsafe {
			t.Fatalf("error code: got %q, want %q", CodeOf(err), ErrorRuntimeTokenUnsafe)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		paths := writeRuntimeFiles(t, descriptor, testToken())
		linkedToken := filepath.Join(t.TempDir(), "token")
		if err := os.Symlink(paths.Token, linkedToken); err != nil {
			t.Fatal(err)
		}
		paths.Token = linkedToken
		_, err := Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }})
		if CodeOf(err) != ErrorRuntimeTokenUnsafe {
			t.Fatalf("error code: got %q, want %q", CodeOf(err), ErrorRuntimeTokenUnsafe)
		}
	})
}

func TestDiscoverStalenessIsOptionalAndProcessAware(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         "http://127.0.0.1:43123",
		DaemonInstanceID: "daemon_test",
		DaemonVersion:    "0.1.0-dev",
		PID:              123,
		ProcessStartedAt: now.AddDate(0, -2, 0),
		GeneratedAt:      now.AddDate(0, -2, 0),
	}, testToken())

	if _, err := Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("old healthy descriptors remain valid by default: %v", err)
	}
	_, err := Discover(paths, DiscoveryPolicy{
		Now:                  func() time.Time { return now },
		MaximumDescriptorAge: time.Hour,
	})
	if CodeOf(err) != ErrorRuntimeDescriptorStale {
		t.Fatalf("maximum-age error code: got %q", CodeOf(err))
	}
	_, err = Discover(paths, DiscoveryPolicy{
		Now: func() time.Time { return now },
		VerifyProcessIdentity: func(pid int, startedAt time.Time) error {
			return errors.New("process identity does not match")
		},
	})
	if CodeOf(err) != ErrorRuntimeDescriptorStale {
		t.Fatalf("process error code: got %q", CodeOf(err))
	}
}

func TestDiscoverNeverReportsTokenContents(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	secret := "this-is-a-secret-token-that-must-not-appear!"
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         "http://127.0.0.1:43123",
		DaemonInstanceID: "daemon_test",
		DaemonVersion:    "0.1.0-dev",
		PID:              123,
		ProcessStartedAt: now,
		GeneratedAt:      now,
	}, secret)

	_, err := Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }})
	if err == nil {
		t.Fatal("expected invalid token error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("error exposed token contents")
	}
}

func TestDiscoverRequiresCanonicalBase64URLToken(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	canonical := testToken()
	nonCanonical := canonical[:len(canonical)-1] + "Z"
	canonicalBytes, err := base64.RawURLEncoding.DecodeString(canonical)
	if err != nil {
		t.Fatal(err)
	}
	nonCanonicalBytes, err := base64.RawURLEncoding.DecodeString(nonCanonical)
	if err != nil || !bytes.Equal(nonCanonicalBytes, canonicalBytes) {
		t.Fatal("test token is not a non-canonical spelling of the canonical token")
	}
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         "http://127.0.0.1:43123",
		DaemonInstanceID: "daemon_test",
		DaemonVersion:    "0.1.0-dev",
		PID:              123,
		ProcessStartedAt: now,
		GeneratedAt:      now,
	}, nonCanonical)

	_, err = Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }})
	if CodeOf(err) != ErrorRuntimeTokenInvalid {
		t.Fatalf("error code: got %q, want %q", CodeOf(err), ErrorRuntimeTokenInvalid)
	}
}

func writeRuntimeFiles(t *testing.T, descriptor RuntimeDescriptor, token string) RuntimePaths {
	t.Helper()
	directory := t.TempDir()
	descriptorPath := filepath.Join(directory, "runtime.json")
	tokenPath := filepath.Join(directory, "auth.token")
	descriptorContents, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(descriptorPath, descriptorContents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return RuntimePaths{Descriptor: descriptorPath, Token: tokenPath}
}

func testToken() string {
	return base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

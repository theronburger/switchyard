package daemon

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

func TestLoadOrCreateTokenCreatesAndReusesPrivateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "auth.token")
	randomBytes := bytes.Repeat([]byte{0x5a}, tokenByteCount)
	token, err := LoadOrCreateToken(path, bytes.NewReader(randomBytes))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := token, base64.RawURLEncoding.EncodeToString(randomBytes); got != want {
		t.Fatalf("token: got %q, want %q", got, want)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("token mode: got %04o, want %04o", got, want)
	}

	reused, err := LoadOrCreateToken(path, bytes.NewReader(bytes.Repeat([]byte{0x33}, tokenByteCount)))
	if err != nil {
		t.Fatal(err)
	}
	if reused != token {
		t.Fatalf("token rotated unexpectedly: got %q, want %q", reused, token)
	}
}

func TestLoadOrCreateTokenRejectsInsecurePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.token")
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, tokenByteCount))
	if err := os.WriteFile(path, []byte(token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateToken(path, nil)
	if !errors.Is(err, ErrInsecureRuntimeFile) {
		t.Fatalf("permission error: got %v, want %v", err, ErrInsecureRuntimeFile)
	}
}

func TestPublishRuntimeDescriptorIsPrivateAndContainsNoToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "endpoint.json")
	startedAt := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	descriptor := RuntimeDescriptor{
		SchemaVersion:    1,
		Endpoint:         "http://127.0.0.1:32123",
		DaemonInstanceID: "daemon_01",
		DaemonVersion:    "0.1.0-dev",
		PID:              123,
		ProcessStartedAt: startedAt,
		GeneratedAt:      startedAt.Add(time.Second),
	}
	if err := PublishRuntimeDescriptor(path, descriptor); err != nil {
		t.Fatal(err)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("descriptor mode: got %04o, want %04o", got, want)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "token") || strings.Contains(string(payload), "Bearer") {
		t.Fatalf("descriptor contains authentication material: %s", payload)
	}
	var decoded RuntimeDescriptor
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != descriptor {
		t.Fatalf("descriptor changed: got %+v, want %+v", decoded, descriptor)
	}
}

func TestPublishRuntimeDescriptorRejectsNonLoopbackEndpoint(t *testing.T) {
	err := PublishRuntimeDescriptor(filepath.Join(t.TempDir(), "endpoint.json"), RuntimeDescriptor{
		SchemaVersion:    1,
		Endpoint:         "http://0.0.0.0:32123",
		DaemonInstanceID: "daemon_01",
		DaemonVersion:    "0.1.0-dev",
		PID:              123,
		ProcessStartedAt: time.Now(),
		GeneratedAt:      time.Now(),
	})
	if err == nil {
		t.Fatal("published a non-loopback endpoint")
	}
}

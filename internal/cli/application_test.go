package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

type stubBackend struct {
	snapshot contractv1.StatusSnapshot
	status   error
	doctor   apiclient.DoctorReport
}

func (b stubBackend) Status(context.Context) (contractv1.StatusSnapshot, error) {
	return b.snapshot, b.status
}

func (b stubBackend) Doctor(context.Context) apiclient.DoctorReport {
	return b.doctor
}

func TestApplicationStatusJSONIsTheStableContractSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	snapshot := contractv1.StatusSnapshot{
		SchemaVersion:    contractv1.SchemaVersion,
		SnapshotRevision: 42,
		GeneratedAt:      now,
		Daemon: contractv1.DaemonStatus{
			InstanceID: "daemon_test",
			Version:    "0.1.0-dev",
			State:      "ready",
			StartedAt:  now,
		},
		Repositories: []contractv1.Repository{},
		Environments: []contractv1.Environment{},
		Operations:   []contractv1.Operation{},
		Alerts:       []contractv1.Alert{},
	}
	var output bytes.Buffer
	application := Application{Backend: stubBackend{snapshot: snapshot}, Stdout: &output}
	if code := application.Run(context.Background(), []string{"status", "--json"}); code != ExitSuccess {
		t.Fatalf("exit code: got %d", code)
	}
	var decoded contractv1.StatusSnapshot
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SnapshotRevision != 42 {
		t.Fatalf("revision: got %d", decoded.SnapshotRevision)
	}
}

func TestApplicationDoctorJSONUsesHealthForExitStatus(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	report := apiclient.DoctorReport{
		SchemaVersion: 1,
		GeneratedAt:   now,
		Healthy:       false,
		Checks: []apiclient.DoctorCheck{{
			ID:        "daemon.handshake",
			Status:    apiclient.CheckFail,
			Summary:   "The installed daemon could not be authenticated.",
			ErrorCode: apiclient.ErrorDaemonUnknown,
		}},
	}
	var output bytes.Buffer
	application := Application{Backend: stubBackend{doctor: report}, Stdout: &output}
	if code := application.Run(context.Background(), []string{"doctor", "--json"}); code != ExitFailure {
		t.Fatalf("exit code: got %d", code)
	}
	var decoded apiclient.DoctorReport
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Checks[0].ErrorCode != apiclient.ErrorDaemonUnknown {
		t.Fatalf("error code: got %q", decoded.Checks[0].ErrorCode)
	}
}

func TestApplicationRedactsBackendErrors(t *testing.T) {
	secret := "secret-bearer-token"
	backend := stubBackend{status: errors.New("request failed with " + secret)}
	for _, jsonOutput := range []bool{false, true} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		arguments := []string{"status"}
		if jsonOutput {
			arguments = append(arguments, "--json")
		}
		application := Application{Backend: backend, Stdout: &stdout, Stderr: &stderr}
		if code := application.Run(context.Background(), arguments); code != ExitFailure {
			t.Fatalf("exit code: got %d", code)
		}
		combined := stdout.String() + stderr.String()
		if strings.Contains(combined, secret) {
			t.Fatal("CLI output leaked backend error contents")
		}
	}
}

func TestApplicationRejectsUnknownArgumentsWithoutEchoingThem(t *testing.T) {
	secretArgument := "--token=secret-value"
	var stderr bytes.Buffer
	application := Application{Backend: stubBackend{}, Stderr: &stderr}
	if code := application.Run(context.Background(), []string{"status", secretArgument}); code != ExitUsage {
		t.Fatalf("exit code: got %d", code)
	}
	if strings.Contains(stderr.String(), secretArgument) {
		t.Fatal("usage output echoed a potentially sensitive argument")
	}
}

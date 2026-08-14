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
	start    func(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error)
	stop     func(context.Context, string, contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error)
}

func (b stubBackend) Status(context.Context) (contractv1.StatusSnapshot, error) {
	return b.snapshot, b.status
}

func (b stubBackend) Doctor(context.Context) apiclient.DoctorReport {
	return b.doctor
}

func (b stubBackend) StartEnvironment(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if b.start == nil {
		return contractv1.MutationReceipt{}, errors.New("start is not configured")
	}
	return b.start(ctx, request)
}

func (b stubBackend) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if b.stop == nil {
		return contractv1.MutationReceipt{}, errors.New("stop is not configured")
	}
	return b.stop(ctx, environmentID, request)
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

func TestApplicationStartBuildsIdempotentMutation(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 14, 15, 30, 0, 0, time.UTC)
	backend := stubBackend{
		start: func(_ context.Context, request contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error) {
			if request.Validate() != nil || request.RequestID != "request_test" ||
				request.IdempotencyKey != "retry-key" || request.WorktreeID != "worktree_01" ||
				len(request.ServiceIDs) != 2 || request.ExpectedEnvironmentRevision == nil ||
				*request.ExpectedEnvironmentRevision != 19 {
				t.Fatalf("start request: %+v", request)
			}
			return cliTestReceipt(request.RequestID, "environment_01", acceptedAt), nil
		},
	}
	var output bytes.Buffer
	application := Application{
		Backend: backend, Stdout: &output,
		NewRequestID: func() (string, error) { return "request_test", nil },
	}
	code := application.Run(context.Background(), []string{
		"start", "worktree_01", "organizer", "nonprofit-service",
		"--expected-revision", "19", "--idempotency-key", "retry-key", "--json",
	})
	if code != ExitSuccess {
		t.Fatalf("exit code: got %d output=%s", code, output.String())
	}
	var receipt contractv1.MutationReceipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.EnvironmentID != "environment_01" || receipt.OperationID == "" {
		t.Fatalf("receipt: %+v", receipt)
	}
}

func TestApplicationStopUsesGeneratedIdempotencyKey(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 14, 15, 30, 0, 0, time.UTC)
	backend := stubBackend{
		stop: func(_ context.Context, environmentID string, request contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error) {
			if environmentID != "environment_01" || request.Validate() != nil ||
				request.RequestID != "request_generated" || request.IdempotencyKey != "cli:request_generated" {
				t.Fatalf("stop environment=%q request=%+v", environmentID, request)
			}
			return cliTestReceipt(request.RequestID, environmentID, acceptedAt), nil
		},
	}
	var output bytes.Buffer
	application := Application{
		Backend: backend, Stdout: &output,
		NewRequestID: func() (string, error) { return "request_generated", nil },
	}
	if code := application.Run(context.Background(), []string{"stop", "environment_01"}); code != ExitSuccess {
		t.Fatalf("exit code: got %d", code)
	}
	if !strings.Contains(output.String(), "operation_01") || !strings.Contains(output.String(), "environment_01") {
		t.Fatalf("stop output: %s", output.String())
	}
}

func TestApplicationRejectsMalformedMutationArgumentsWithoutBackendCall(t *testing.T) {
	calls := 0
	backend := stubBackend{
		start: func(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error) {
			calls++
			return contractv1.MutationReceipt{}, nil
		},
		stop: func(context.Context, string, contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error) {
			calls++
			return contractv1.MutationReceipt{}, nil
		},
	}
	tests := [][]string{
		{"start", "worktree-only"},
		{"stop"},
		{"stop", "environment", "extra"},
		{"start", "worktree", "service", "--expected-revision", "-1"},
		{"start", "worktree", "service", "--json", "--json"},
	}
	for _, arguments := range tests {
		var stderr bytes.Buffer
		application := Application{Backend: backend, Stderr: &stderr}
		if code := application.Run(context.Background(), arguments); code != ExitUsage {
			t.Fatalf("arguments=%v exit=%d", arguments, code)
		}
	}
	if calls != 0 {
		t.Fatalf("malformed arguments reached backend %d times", calls)
	}
}

func cliTestReceipt(requestID, environmentID string, acceptedAt time.Time) contractv1.MutationReceipt {
	return contractv1.MutationReceipt{
		SchemaVersion: contractv1.SchemaVersion,
		RequestID:     requestID, OperationID: "operation_01", AcceptedAt: acceptedAt, EnvironmentID: environmentID,
	}
}

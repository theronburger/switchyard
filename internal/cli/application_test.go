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
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/control/statusview"
)

type stubBackend struct {
	snapshot   contractv2.StatusSnapshot
	status     error
	readStatus func() (contractv2.StatusSnapshot, error)
	doctor     apiclient.DoctorReport
	start      func(context.Context, contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error)
	stop       func(context.Context, string, contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error)
	create     func(context.Context, contractv2.CreateWorktreeRequest) (contractv2.MutationReceipt, error)
	adopt      func(context.Context, contractv2.AdoptWorktreeRequest) (contractv2.MutationReceipt, error)
	archive    func(context.Context, contractv2.ArchiveWorktreeRequest) (contractv2.MutationReceipt, error)
	prepare    func(context.Context, contractv2.PrepareWorktreeRequest) (contractv2.MutationReceipt, error)
}

func (b stubBackend) CreateWorktree(
	ctx context.Context,
	request contractv2.CreateWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if b.create == nil {
		return contractv2.MutationReceipt{}, errors.New("create is not configured")
	}
	return b.create(ctx, request)
}

func (b stubBackend) ArchiveWorktree(
	ctx context.Context,
	request contractv2.ArchiveWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if b.archive == nil {
		return contractv2.MutationReceipt{}, errors.New("archive is not configured")
	}
	return b.archive(ctx, request)
}

func (b stubBackend) AdoptWorktree(
	ctx context.Context,
	request contractv2.AdoptWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if b.adopt == nil {
		return contractv2.MutationReceipt{}, errors.New("adopt is not configured")
	}
	return b.adopt(ctx, request)
}

func (b stubBackend) PrepareWorktree(
	ctx context.Context,
	request contractv2.PrepareWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if b.prepare == nil {
		return contractv2.MutationReceipt{}, errors.New("prepare is not configured")
	}
	return b.prepare(ctx, request)
}

func (b stubBackend) Status(context.Context) (contractv2.StatusSnapshot, error) {
	if b.readStatus != nil {
		return b.readStatus()
	}
	return b.snapshot, b.status
}

func (b stubBackend) Doctor(context.Context) apiclient.DoctorReport {
	return b.doctor
}

func (b stubBackend) StartEnvironment(
	ctx context.Context,
	request contractv2.StartEnvironmentRequest,
) (contractv2.MutationReceipt, error) {
	if b.start == nil {
		return contractv2.MutationReceipt{}, errors.New("start is not configured")
	}
	return b.start(ctx, request)
}

func (b stubBackend) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv2.StopEnvironmentRequest,
) (contractv2.MutationReceipt, error) {
	if b.stop == nil {
		return contractv2.MutationReceipt{}, errors.New("stop is not configured")
	}
	return b.stop(ctx, environmentID, request)
}

func TestApplicationStatusJSONIsTheStableContractSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	snapshot := contractv2.StatusSnapshot{
		SchemaVersion:    contractv2.SchemaVersion,
		SnapshotRevision: 42,
		GeneratedAt:      now,
		Daemon: contractv2.DaemonStatus{
			InstanceID: "daemon_test",
			Version:    "0.1.0-dev",
			State:      "ready",
			StartedAt:  now,
		},
		Repositories: []contractv2.Repository{},
		Environments: []contractv2.Environment{},
		Operations:   []contractv2.Operation{},
		Alerts:       []contractv2.Alert{},
	}
	var output bytes.Buffer
	application := Application{Backend: stubBackend{snapshot: snapshot}, Stdout: &output}
	if code := application.Run(context.Background(), []string{"status", "--all", "--json"}); code != ExitSuccess {
		t.Fatalf("exit code: got %d", code)
	}
	var decoded contractv2.StatusSnapshot
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SnapshotRevision != 42 {
		t.Fatalf("revision: got %d", decoded.SnapshotRevision)
	}
}

func TestApplicationStatusDefaultsToContainingWorktree(t *testing.T) {
	snapshot := cliStatusSnapshot()
	var output bytes.Buffer
	application := Application{
		Backend: stubBackend{snapshot: snapshot}, Stdout: &output,
		Getwd: func() (string, error) { return "/Developer/worktrees/feature-a/services/api", nil },
	}
	if code := application.Run(context.Background(), []string{"status"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	text := output.String()
	if !strings.Contains(text, "feature/a") || !strings.Contains(text, "environment_feature") ||
		strings.Contains(text, "environment_primary") || strings.Contains(text, "Environments: 2") {
		t.Fatalf("scoped status:\n%s", text)
	}
}

func TestApplicationStatusJSONReturnsWorktreeContextAndAllReturnsInventory(t *testing.T) {
	snapshot := cliStatusSnapshot()
	application := Application{
		Backend: stubBackend{snapshot: snapshot},
		Getwd:   func() (string, error) { return "/Developer/worktrees/feature-a", nil },
	}
	var scoped bytes.Buffer
	application.Stdout = &scoped
	if code := application.Run(context.Background(), []string{"status", "--json"}); code != ExitSuccess {
		t.Fatalf("scoped exit code: %d", code)
	}
	var contextView statusview.WorktreeContext
	if err := json.Unmarshal(scoped.Bytes(), &contextView); err != nil {
		t.Fatal(err)
	}
	if contextView.Worktree.ID != "worktree_feature" || len(contextView.Environments) != 1 {
		t.Fatalf("context: %+v", contextView)
	}

	var inventory bytes.Buffer
	application.Stdout = &inventory
	if code := application.Run(context.Background(), []string{"status", "--all", "--json"}); code != ExitSuccess {
		t.Fatalf("inventory exit code: %d", code)
	}
	var all contractv2.StatusSnapshot
	if err := json.Unmarshal(inventory.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Environments) != 2 {
		t.Fatalf("inventory environments: %d", len(all.Environments))
	}
}

func TestApplicationStatusAcceptsExactBranchAndFallsBackOutsideKnownTrees(t *testing.T) {
	snapshot := cliStatusSnapshot()
	var selected bytes.Buffer
	application := Application{Backend: stubBackend{snapshot: snapshot}, Stdout: &selected}
	if code := application.Run(context.Background(), []string{"status", "feature/a"}); code != ExitSuccess {
		t.Fatalf("selector exit code: %d", code)
	}
	if !strings.Contains(selected.String(), "feature/a") || strings.Contains(selected.String(), "environment_primary") {
		t.Fatalf("selector output:\n%s", selected.String())
	}

	var fallback bytes.Buffer
	application.Stdout = &fallback
	application.Getwd = func() (string, error) { return "/Developer/unrelated", nil }
	if code := application.Run(context.Background(), []string{"status"}); code != ExitSuccess {
		t.Fatalf("fallback exit code: %d", code)
	}
	if !strings.Contains(fallback.String(), "No known worktree") || !strings.Contains(fallback.String(), "Environments: 2") {
		t.Fatalf("fallback output:\n%s", fallback.String())
	}

	var jsonFailure bytes.Buffer
	application.Stdout = &jsonFailure
	if code := application.Run(context.Background(), []string{"status", "--json"}); code != ExitFailure {
		t.Fatalf("unknown JSON scope exit code: %d", code)
	}
	if !strings.Contains(jsonFailure.String(), `"code":"WORKTREE_NOT_FOUND"`) {
		t.Fatalf("unknown JSON scope: %s", jsonFailure.String())
	}
}

func TestApplicationDoctorJSONUsesHealthForExitStatus(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	report := apiclient.DoctorReport{
		SchemaVersion: contractv2.SchemaVersion,
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
		start: func(_ context.Context, request contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
			if request.Validate() != nil || request.RequestID != "request_test" ||
				request.IdempotencyKey != "retry-key" || request.WorktreeID != "worktree_01" ||
				request.TargetID != "demo" || request.ConfirmedTargetID != "demo" ||
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
		"start", "worktree_01", "storefront", "billing-service",
		"--target", "demo", "--confirm-target", "demo", "--expected-revision", "19", "--idempotency-key", "retry-key", "--json",
	})
	if code != ExitSuccess {
		t.Fatalf("exit code: got %d output=%s", code, output.String())
	}
	var receipt contractv2.MutationReceipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.EnvironmentID != "environment_01" || receipt.OperationID == "" {
		t.Fatalf("receipt: %+v", receipt)
	}
}

func TestApplicationStartDotResolvesTheContainingWorktree(t *testing.T) {
	backend := stubBackend{
		snapshot: cliStatusSnapshot(),
		start: func(_ context.Context, request contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
			if request.WorktreeID != "worktree_feature" || request.TargetID != "testing" ||
				len(request.ServiceIDs) != 2 || request.ServiceIDs[0] != "app" || request.ServiceIDs[1] != "api" {
				t.Fatalf("start request: %+v", request)
			}
			return cliTestReceipt(request.RequestID, "environment_feature", time.Now()), nil
		},
	}
	application := Application{
		Backend:      backend,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a/services/api", nil },
		NewRequestID: func() (string, error) { return "request_current", nil },
	}
	if code := application.Run(context.Background(), []string{
		"start", ".", "app", "api", "--target", "testing",
	}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
}

func TestApplicationStartDotWaitRetriesDiscoveryAndDaemonRestart(t *testing.T) {
	snapshot := cliStatusSnapshot()
	statusCalls := 0
	startCalls := 0
	backend := stubBackend{
		readStatus: func() (contractv2.StatusSnapshot, error) {
			statusCalls++
			if statusCalls == 1 {
				discoveryPending := snapshot
				discoveryPending.Repositories = []contractv2.Repository{snapshot.Repositories[0]}
				discoveryPending.Repositories[0].Worktrees = []contractv2.Worktree{snapshot.Repositories[0].Worktrees[0]}
				return discoveryPending, nil
			}
			if statusCalls == 2 {
				return snapshot, nil
			}
			completed := snapshot
			completed.Environments[1].DesiredState = "running"
			completed.Environments[1].ObservedState = "running"
			completed.Environments[1].Health = "healthy"
			completed.Environments[1].Services = []contractv2.Service{
				{ID: "app", DesiredState: "running", ObservedState: "running", Health: "healthy", Run: &contractv2.ServiceRun{ID: "run_start"}},
				{ID: "api", DesiredState: "running", ObservedState: "running", Health: "healthy", Run: &contractv2.ServiceRun{ID: "run_start"}},
			}
			completed.Operations = []contractv2.Operation{{ID: "operation_start", State: "succeeded"}}
			return completed, nil
		},
		start: func(_ context.Context, request contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
			startCalls++
			if startCalls == 1 {
				return contractv2.MutationReceipt{}, &apiclient.CodedError{Code: apiclient.ErrorDaemonUnavailable}
			}
			return contractv2.MutationReceipt{
				SchemaVersion: contractv2.SchemaVersion, RequestID: request.RequestID, OperationID: "operation_start",
				RunID: "run_start", AcceptedAt: time.Now(), EnvironmentID: "environment_feature",
			}, nil
		},
	}
	application := Application{
		Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a", nil },
		NewRequestID: func() (string, error) { return "request_start", nil },
	}
	if code := application.Run(context.Background(), []string{"start", ".", "app", "api", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if statusCalls != 3 || startCalls != 2 {
		t.Fatalf("status calls=%d start calls=%d", statusCalls, startCalls)
	}
}

func TestApplicationPrepareDotHydratesCurrentWorktreeAndWaitsForReadiness(t *testing.T) {
	snapshot := cliStatusSnapshot()
	statusCalls := 0
	backend := stubBackend{
		readStatus: func() (contractv2.StatusSnapshot, error) {
			statusCalls++
			if statusCalls == 1 {
				discoveryPending := snapshot
				discoveryPending.Repositories = []contractv2.Repository{snapshot.Repositories[0]}
				discoveryPending.Repositories[0].Worktrees = []contractv2.Worktree{snapshot.Repositories[0].Worktrees[0]}
				return discoveryPending, nil
			}
			if statusCalls == 2 {
				return snapshot, nil
			}
			completed := snapshot
			completed.Repositories[0].Worktrees[1].Workspace = &contractv2.WorkspaceStatus{
				Ownership: "adopted", State: "ready", Fingerprint: "fingerprint_01", PreparedAt: time.Now(),
				Toolchains: []contractv2.WorkspaceToolchain{},
			}
			completed.Operations = []contractv2.Operation{{ID: "operation_01", Kind: "workspace.prepare", State: "succeeded"}}
			return completed, nil
		},
		prepare: func(_ context.Context, request contractv2.PrepareWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.Validate() != nil || request.WorktreeID != "worktree_feature" ||
				request.IdempotencyKey != "cli:request_prepare" {
				t.Fatalf("prepare request: %+v", request)
			}
			return cliTestReceipt(request.RequestID, "", time.Now()), nil
		},
	}
	application := Application{
		Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a/services/api", nil },
		NewRequestID: func() (string, error) { return "request_prepare", nil },
	}
	if code := application.Run(context.Background(), []string{"prepare", ".", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if statusCalls != 3 {
		t.Fatalf("status calls: %d", statusCalls)
	}
}

func TestApplicationPrepareWaitRetriesAcrossDaemonInventoryRestart(t *testing.T) {
	snapshot := cliStatusSnapshot()
	prepareCalls := 0
	backend := stubBackend{
		snapshot: snapshot,
		prepare: func(_ context.Context, request contractv2.PrepareWorktreeRequest) (contractv2.MutationReceipt, error) {
			prepareCalls++
			if prepareCalls == 1 {
				return contractv2.MutationReceipt{}, &apiclient.CodedError{Code: apiclient.ErrorDaemonUnavailable}
			}
			return cliTestReceipt(request.RequestID, "", time.Now()), nil
		},
		readStatus: func() (contractv2.StatusSnapshot, error) {
			ready := snapshot
			ready.Repositories[0].Worktrees[1].Workspace = &contractv2.WorkspaceStatus{
				Ownership: "adopted", State: "ready", Fingerprint: "fingerprint_01", PreparedAt: time.Now(),
				Toolchains: []contractv2.WorkspaceToolchain{},
			}
			ready.Operations = []contractv2.Operation{{ID: "operation_01", State: "succeeded"}}
			return ready, nil
		},
	}
	application := Application{
		Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a", nil },
		NewRequestID: func() (string, error) { return "request_prepare", nil },
	}
	if code := application.Run(context.Background(), []string{"prepare", ".", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if prepareCalls != 2 {
		t.Fatalf("prepare calls: %d", prepareCalls)
	}
}

func TestApplicationPrepareWaitResubmitsAfterInterruptedOperation(t *testing.T) {
	snapshot := cliStatusSnapshot()
	statusCalls := 0
	prepareCalls := 0
	requestIDs := []string{"request_prepare_1", "request_prepare_2"}
	requestIndex := 0
	backend := stubBackend{
		readStatus: func() (contractv2.StatusSnapshot, error) {
			statusCalls++
			if statusCalls == 1 {
				return snapshot, nil
			}
			observed := snapshot
			if statusCalls == 2 {
				observed.Operations = []contractv2.Operation{{
					ID: "operation_prepare_1", State: "failed",
					Error: &contractv2.ContractError{
						Code: "DAEMON_RESTARTED", Message: "The daemon restarted before the operation completed.", Retryable: true,
					},
				}}
				return observed, nil
			}
			observed.Repositories[0].Worktrees[1].Workspace = &contractv2.WorkspaceStatus{
				Ownership: "adopted", State: "ready", Fingerprint: "fingerprint_01", PreparedAt: time.Now(),
				Toolchains: []contractv2.WorkspaceToolchain{},
			}
			observed.Operations = []contractv2.Operation{{ID: "operation_prepare_2", State: "succeeded"}}
			return observed, nil
		},
		prepare: func(_ context.Context, request contractv2.PrepareWorktreeRequest) (contractv2.MutationReceipt, error) {
			prepareCalls++
			expectedRequestID := requestIDs[prepareCalls-1]
			if request.RequestID != expectedRequestID || request.IdempotencyKey != "cli:"+expectedRequestID {
				t.Fatalf("prepare request: %+v", request)
			}
			return contractv2.MutationReceipt{
				SchemaVersion: contractv2.SchemaVersion, RequestID: request.RequestID,
				OperationID: "operation_prepare_" + string(rune('0'+prepareCalls)), AcceptedAt: time.Now(),
			}, nil
		},
	}
	application := Application{
		Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second,
		Getwd: func() (string, error) { return "/Developer/worktrees/feature-a", nil },
		NewRequestID: func() (string, error) {
			requestID := requestIDs[requestIndex]
			requestIndex++
			return requestID, nil
		},
	}
	if code := application.Run(context.Background(), []string{"prepare", ".", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if prepareCalls != 2 || statusCalls != 3 {
		t.Fatalf("prepare calls=%d status calls=%d", prepareCalls, statusCalls)
	}
}

func TestApplicationPrepareWaitEmitsStructuredFailure(t *testing.T) {
	snapshot := cliStatusSnapshot()
	statusCalls := 0
	backend := stubBackend{
		readStatus: func() (contractv2.StatusSnapshot, error) {
			statusCalls++
			if statusCalls == 1 {
				return snapshot, nil
			}
			failed := snapshot
			failed.Operations = []contractv2.Operation{{
				ID: "operation_prepare", State: "failed", Error: &contractv2.ContractError{
					Code: "WORKSPACE_NOT_READY", Message: "The workspace could not be verified.",
					Retryable: true, NextAction: "inspect_workspace_diagnostics",
				},
			}}
			return failed, nil
		},
		prepare: func(_ context.Context, request contractv2.PrepareWorktreeRequest) (contractv2.MutationReceipt, error) {
			return contractv2.MutationReceipt{
				SchemaVersion: contractv2.SchemaVersion, RequestID: request.RequestID,
				OperationID: "operation_prepare", AcceptedAt: time.Now(),
			}, nil
		},
	}
	var output bytes.Buffer
	application := Application{
		Backend: backend, Stdout: &output, PollInterval: time.Millisecond, WaitTimeout: time.Second,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a", nil },
		NewRequestID: func() (string, error) { return "request_prepare", nil },
	}
	if code := application.Run(context.Background(), []string{
		"prepare", ".", "--wait", "--json", "--idempotency-key", "prepare:explicit",
	}); code != ExitFailure {
		t.Fatalf("exit code: %d", code)
	}
	var failure errorOutput
	if err := json.Unmarshal(output.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != apiclient.ErrorCode("WORKSPACE_NOT_READY") ||
		failure.Error.NextAction != "inspect_workspace_diagnostics" {
		t.Fatalf("failure: %+v", failure)
	}
}

func TestApplicationStopUsesGeneratedIdempotencyKey(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 14, 15, 30, 0, 0, time.UTC)
	backend := stubBackend{
		stop: func(_ context.Context, environmentID string, request contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
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

func TestApplicationStopDotWaitsForOwnedResourcesToDisappear(t *testing.T) {
	snapshot := cliStatusSnapshot()
	snapshot.Environments[1].Revision = 23
	snapshot.Environments[1].PortLeases = []contractv2.PortLease{{ID: "port_feature"}}
	statusCalls := 0
	backend := stubBackend{
		readStatus: func() (contractv2.StatusSnapshot, error) {
			statusCalls++
			if statusCalls == 1 {
				return snapshot, nil
			}
			completed := snapshot
			completed.Environments[1].DesiredState = "stopped"
			completed.Environments[1].ObservedState = "stopped"
			completed.Environments[1].PortLeases = []contractv2.PortLease{}
			completed.Environments[1].InfrastructureLeases = []contractv2.InfrastructureLease{}
			completed.Operations = []contractv2.Operation{{
				ID: "operation_01", EnvironmentID: "environment_feature", State: "succeeded",
			}}
			return completed, nil
		},
		stop: func(_ context.Context, environmentID string, request contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
			if environmentID != "environment_feature" || request.ExpectedEnvironmentRevision != nil {
				t.Fatalf("stop environment=%q request=%+v", environmentID, request)
			}
			return cliTestReceipt(request.RequestID, environmentID, time.Now()), nil
		},
	}
	application := Application{
		Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a", nil },
		NewRequestID: func() (string, error) { return "request_cleanup", nil },
	}
	if code := application.Run(context.Background(), []string{"stop", ".", "--if-running", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if statusCalls != 2 {
		t.Fatalf("status calls: %d", statusCalls)
	}
}

func TestApplicationStopDotWaitRetriesDiscoveryAndSubmissionAcrossDaemonRestart(t *testing.T) {
	snapshot := cliStatusSnapshot()
	statusCalls := 0
	stopCalls := 0
	backend := stubBackend{
		readStatus: func() (contractv2.StatusSnapshot, error) {
			statusCalls++
			if statusCalls == 1 {
				return contractv2.StatusSnapshot{}, &apiclient.CodedError{Code: apiclient.ErrorDaemonUnavailable}
			}
			if statusCalls == 2 {
				return snapshot, nil
			}
			completed := snapshot
			completed.Environments[1].DesiredState = "stopped"
			completed.Environments[1].ObservedState = "stopped"
			completed.Environments[1].PortLeases = []contractv2.PortLease{}
			completed.Environments[1].InfrastructureLeases = []contractv2.InfrastructureLease{}
			completed.Operations = []contractv2.Operation{{ID: "operation_stop", State: "succeeded"}}
			return completed, nil
		},
		stop: func(_ context.Context, environmentID string, request contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
			stopCalls++
			if environmentID != "environment_feature" || request.RequestID != "request_cleanup" ||
				request.IdempotencyKey != "cli:request_cleanup" {
				t.Fatalf("stop environment=%q request=%+v", environmentID, request)
			}
			if stopCalls == 1 {
				return contractv2.MutationReceipt{}, &apiclient.CodedError{Code: apiclient.ErrorDaemonUnavailable}
			}
			return contractv2.MutationReceipt{
				SchemaVersion: contractv2.SchemaVersion, RequestID: request.RequestID,
				OperationID: "operation_stop", EnvironmentID: environmentID, AcceptedAt: time.Now(),
			}, nil
		},
	}
	application := Application{
		Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a", nil },
		NewRequestID: func() (string, error) { return "request_cleanup", nil },
	}
	if code := application.Run(context.Background(), []string{"stop", ".", "--if-running", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if statusCalls != 3 || stopCalls != 2 {
		t.Fatalf("status calls=%d stop calls=%d", statusCalls, stopCalls)
	}
}

func TestApplicationStopDotPreservesExplicitExpectedRevision(t *testing.T) {
	snapshot := cliStatusSnapshot()
	backend := stubBackend{
		snapshot: snapshot,
		stop: func(_ context.Context, environmentID string, request contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
			if environmentID != "environment_feature" || request.ExpectedEnvironmentRevision == nil ||
				*request.ExpectedEnvironmentRevision != 17 {
				t.Fatalf("stop environment=%q request=%+v", environmentID, request)
			}
			return cliTestReceipt(request.RequestID, environmentID, time.Now()), nil
		},
	}
	application := Application{
		Backend: backend, Getwd: func() (string, error) { return "/Developer/worktrees/feature-a", nil },
		NewRequestID: func() (string, error) { return "request_cleanup", nil },
	}
	if code := application.Run(context.Background(), []string{"stop", ".", "--expected-revision", "17"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
}

func TestApplicationStopDotIfRunningIsAnIdempotentNoOp(t *testing.T) {
	snapshot := cliStatusSnapshot()
	snapshot.Environments = snapshot.Environments[:1]
	stopCalls := 0
	var output bytes.Buffer
	application := Application{
		Backend: stubBackend{
			snapshot: snapshot,
			stop: func(context.Context, string, contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
				stopCalls++
				return contractv2.MutationReceipt{}, nil
			},
		},
		Stdout: &output,
		Getwd:  func() (string, error) { return "/Developer/worktrees/feature-a", nil },
	}
	if code := application.Run(context.Background(), []string{"stop", ".", "--if-running", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if stopCalls != 0 || !strings.Contains(output.String(), "No Switchyard environment is running") {
		t.Fatalf("stop calls=%d output=%q", stopCalls, output.String())
	}
}

func TestApplicationStopDotIfRunningJSONEmitsNoOpResult(t *testing.T) {
	snapshot := cliStatusSnapshot()
	snapshot.Environments = snapshot.Environments[:1]
	var output bytes.Buffer
	application := Application{
		Backend: stubBackend{snapshot: snapshot}, Stdout: &output,
		Getwd: func() (string, error) { return "/Developer/worktrees/feature-a", nil },
	}
	if code := application.Run(context.Background(), []string{"stop", ".", "--if-running", "--json"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	var result noRunningEnvironmentOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != contractv2.SchemaVersion || result.Outcome != "alreadyStopped" {
		t.Fatalf("result: %+v", result)
	}
}

func TestApplicationCreatesAdoptsAndArchivesManagedWorktrees(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	backend := stubBackend{
		create: func(_ context.Context, request contractv2.CreateWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.Validate() != nil || request.RepositoryID != "repository_01" ||
				request.Branch != "feature/go-service" || request.StartPoint != "origin/main" {
				t.Fatalf("create request: %+v", request)
			}
			return cliTestReceipt(request.RequestID, "", acceptedAt), nil
		},
		adopt: func(_ context.Context, request contractv2.AdoptWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.Validate() != nil || request.WorktreeID != "worktree_01" {
				t.Fatalf("adopt request: %+v", request)
			}
			return cliTestReceipt(request.RequestID, "", acceptedAt), nil
		},
		archive: func(_ context.Context, request contractv2.ArchiveWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.Validate() != nil || request.WorktreeID != "worktree_01" {
				t.Fatalf("archive request: %+v", request)
			}
			return cliTestReceipt(request.RequestID, "", acceptedAt), nil
		},
	}
	requestIndex := 0
	application := Application{
		Backend: backend,
		NewRequestID: func() (string, error) {
			requestIndex++
			return "request_" + string(rune('0'+requestIndex)), nil
		},
	}
	if code := application.Run(context.Background(), []string{
		"create-worktree", "repository_01", "feature/go-service", "--base", "origin/main",
	}); code != ExitSuccess {
		t.Fatalf("create exit code: %d", code)
	}
	if code := application.Run(context.Background(), []string{"adopt-worktree", "worktree_01"}); code != ExitSuccess {
		t.Fatalf("adopt exit code: %d", code)
	}
	if code := application.Run(context.Background(), []string{"archive-worktree", "worktree_01"}); code != ExitSuccess {
		t.Fatalf("archive exit code: %d", code)
	}
}

func TestApplicationRejectsMalformedMutationArgumentsWithoutBackendCall(t *testing.T) {
	calls := 0
	backend := stubBackend{
		start: func(context.Context, contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
			calls++
			return contractv2.MutationReceipt{}, nil
		},
		stop: func(context.Context, string, contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
			calls++
			return contractv2.MutationReceipt{}, nil
		},
	}
	tests := [][]string{
		{"status", "feature/a", "--all"},
		{"start", "worktree-only"},
		{"stop"},
		{"stop", "environment", "extra"},
		{"start", "worktree", "service", "--expected-revision", "-1"},
		{"start", "worktree", "service", "--json", "--json"},
		{"stop", "environment", "--all"},
		{"start", "worktree", "service", "--if-running"},
		{"status", "--wait"},
		{"stop", "environment", "--wait", "--wait"},
		{"stop", "environment", "--if-running"},
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

func cliTestReceipt(requestID, environmentID string, acceptedAt time.Time) contractv2.MutationReceipt {
	return contractv2.MutationReceipt{
		SchemaVersion: contractv2.SchemaVersion,
		RequestID:     requestID, OperationID: "operation_01", AcceptedAt: acceptedAt, EnvironmentID: environmentID,
	}
}

func cliStatusSnapshot() contractv2.StatusSnapshot {
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	return contractv2.StatusSnapshot{
		SchemaVersion: contractv2.SchemaVersion, SnapshotRevision: 19, GeneratedAt: now,
		Daemon: contractv2.DaemonStatus{InstanceID: "daemon_test", Version: "test", State: "ready", StartedAt: now},
		Repositories: []contractv2.Repository{{
			ID: "repository_test", DisplayName: "sample", RootPath: "/Developer/sample", ProfileKey: "sample",
			Worktrees: []contractv2.Worktree{
				{ID: "worktree_primary", Path: "/Developer/sample", Branch: "main", HeadRevision: "aaaaaaaaaaaaaaaa", IsPrimary: true},
				{ID: "worktree_feature", Path: "/Developer/worktrees/feature-a", Branch: "feature/a", HeadRevision: "bbbbbbbbbbbbbbbb"},
			},
		}},
		Environments: []contractv2.Environment{
			{ID: "environment_primary", RepositoryID: "repository_test", WorktreeID: "worktree_primary", DisplayName: "main", ObservedState: "stopped", Health: "unknown", Services: []contractv2.Service{}, PortLeases: []contractv2.PortLease{}, InfrastructureLeases: []contractv2.InfrastructureLease{}, URLs: map[string]string{}, AttentionAlertIDs: []string{}},
			{ID: "environment_feature", RepositoryID: "repository_test", WorktreeID: "worktree_feature", DisplayName: "feature/a", TargetID: "testing", ObservedState: "running", Health: "healthy", Services: []contractv2.Service{}, PortLeases: []contractv2.PortLease{}, InfrastructureLeases: []contractv2.InfrastructureLease{}, URLs: map[string]string{}, AttentionAlertIDs: []string{}},
		},
		Operations: []contractv2.Operation{}, Alerts: []contractv2.Alert{},
	}
}

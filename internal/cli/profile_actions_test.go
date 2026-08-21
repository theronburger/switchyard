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
)

type actionStubBackend struct {
	stubBackend
	list func(context.Context) (contractv2.ProfileActionList, error)
	run  func(context.Context, contractv2.RunProfileActionRequest) (contractv2.MutationReceipt, error)
}

func (b actionStubBackend) ListProfileActions(ctx context.Context) (contractv2.ProfileActionList, error) {
	if b.list == nil {
		return contractv2.ProfileActionList{}, errors.New("list is not configured")
	}
	return b.list(ctx)
}

func (b actionStubBackend) RunProfileAction(ctx context.Context, request contractv2.RunProfileActionRequest) (contractv2.MutationReceipt, error) {
	if b.run == nil {
		return contractv2.MutationReceipt{}, errors.New("run is not configured")
	}
	return b.run(ctx, request)
}

func TestApplicationActionsListsAcceptedActions(t *testing.T) {
	backend := actionStubBackend{list: func(context.Context) (contractv2.ProfileActionList, error) {
		return contractv2.ProfileActionList{
			SchemaVersion: contractv2.SchemaVersion, Actions: []contractv2.ProfileAction{{
				ID: "push", RepositoryID: "repository_01", ProfileKey: "sample", ProfileDigest: "sha256:" + strings.Repeat("a", 64),
				DisplayName: "Push", Scope: "repository", Risk: "remote-write", Kind: "command", RequiresConfirmation: true,
			}},
		}, nil
	}}
	var output bytes.Buffer
	if code := (Application{Backend: backend, Stdout: &output}).Run(context.Background(), []string{"actions"}); code != ExitSuccess {
		t.Fatalf("exit code: %d %s", code, output.String())
	}
	if !strings.Contains(output.String(), "repository_01 push") || !strings.Contains(output.String(), "confirmation required") {
		t.Fatalf("output: %s", output.String())
	}
	output.Reset()
	if code := (Application{Backend: backend, Stdout: &output}).Run(context.Background(), []string{"actions", "--json"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	var list contractv2.ProfileActionList
	if err := json.Unmarshal(output.Bytes(), &list); err != nil || len(list.Actions) != 1 {
		t.Fatalf("json output: %v %s", err, output.String())
	}
	if code := (Application{Backend: stubBackend{}, Stdout: &output}).Run(context.Background(), []string{"actions"}); code != ExitFailure {
		t.Fatalf("backend without actions: %d", code)
	}
}

func TestApplicationActionBuildsScopedRequest(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	backend := actionStubBackend{
		stubBackend: stubBackend{snapshot: cliStatusSnapshot()},
		run: func(_ context.Context, request contractv2.RunProfileActionRequest) (contractv2.MutationReceipt, error) {
			if request.Validate() != nil || request.RepositoryID != "repository_01" || request.ActionID != "push" ||
				request.ConfirmedActionID != "push" || request.IdempotencyKey != "retry-key" || request.RequestID != "request_test" ||
				request.WorktreeID != "" || request.EnvironmentID != "" {
				t.Fatalf("request: %+v", request)
			}
			return cliTestReceipt(request.RequestID, "", acceptedAt), nil
		},
	}
	var output bytes.Buffer
	application := Application{Backend: backend, Stdout: &output, NewRequestID: func() (string, error) { return "request_test", nil }}
	code := application.Run(context.Background(), []string{
		"action", "repository_01", "push", "--confirm-action", "push", "--idempotency-key", "retry-key", "--json",
	})
	if code != ExitSuccess {
		t.Fatalf("exit code: %d %s", code, output.String())
	}
	var receipt contractv2.MutationReceipt
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil || receipt.OperationID != "operation_01" {
		t.Fatalf("receipt: %v %s", err, output.String())
	}
}

func TestApplicationActionDotResolvesCurrentWorktreeAndWaits(t *testing.T) {
	snapshot := cliStatusSnapshot()
	var submitted contractv2.RunProfileActionRequest
	backend := actionStubBackend{
		stubBackend: stubBackend{readStatus: func() (contractv2.StatusSnapshot, error) {
			current := snapshot
			if submitted.RequestID != "" {
				current.Operations = append(current.Operations, contractv2.Operation{
					ID: "operation_01", Kind: "profile.action", State: "succeeded",
					CreatedAt: snapshot.GeneratedAt, UpdatedAt: snapshot.GeneratedAt,
				})
			}
			return current, nil
		}},
		run: func(_ context.Context, request contractv2.RunProfileActionRequest) (contractv2.MutationReceipt, error) {
			submitted = request
			return cliTestReceipt(request.RequestID, "", time.Now()), nil
		},
	}
	application := Application{
		Backend: backend, PollInterval: time.Millisecond,
		Getwd:        func() (string, error) { return "/Developer/worktrees/feature-a/services/api", nil },
		NewRequestID: func() (string, error) { return "request_current", nil },
	}
	if code := application.Run(context.Background(), []string{"action", "repository_01", "tidy", "--worktree", ".", "--wait"}); code != ExitSuccess {
		t.Fatalf("exit code: %d", code)
	}
	if submitted.WorktreeID != "worktree_feature" || submitted.IdempotencyKey != "cli:request_current" {
		t.Fatalf("submitted: %+v", submitted)
	}
}

func TestApplicationActionWaitReportsStructuredFailure(t *testing.T) {
	snapshot := cliStatusSnapshot()
	exitCode := 7
	backend := actionStubBackend{
		stubBackend: stubBackend{readStatus: func() (contractv2.StatusSnapshot, error) {
			current := snapshot
			current.Operations = append(current.Operations, contractv2.Operation{
				ID: "operation_01", Kind: "profile.action", State: "failed",
				CreatedAt: snapshot.GeneratedAt, UpdatedAt: snapshot.GeneratedAt,
				Error: &contractv2.ContractError{
					Code: "ACTION_COMMAND_FAILED", Message: "The profile action command exited unsuccessfully.",
					ResourceKind: "action", ResourceID: "tidy", ExitCode: &exitCode, LogReference: "sample/operation_01",
				},
			})
			return current, nil
		}},
		run: func(_ context.Context, request contractv2.RunProfileActionRequest) (contractv2.MutationReceipt, error) {
			return cliTestReceipt(request.RequestID, "", time.Now()), nil
		},
	}
	var stdout bytes.Buffer
	application := Application{Backend: backend, Stdout: &stdout, PollInterval: time.Millisecond}
	code := application.Run(context.Background(), []string{"action", "repository_01", "tidy", "--worktree", "worktree_feature", "--wait", "--json"})
	if code != ExitFailure {
		t.Fatalf("exit code: %d %s", code, stdout.String())
	}
	var failure errorOutput
	if err := json.Unmarshal(stdout.Bytes(), &failure); err != nil || failure.Error.Code != apiclient.ErrorCode("ACTION_COMMAND_FAILED") ||
		failure.Error.ExitCode == nil || *failure.Error.ExitCode != 7 {
		t.Fatalf("failure output: %v %s", err, stdout.String())
	}
}

func TestParseArgumentsRejectsMalformedActionInvocations(t *testing.T) {
	rejected := [][]string{
		{"action"},
		{"action", "repository_01"},
		{"action", "repository_01", "tidy", "extra"},
		{"action", "repository_01", "tidy", "--worktree", "w", "--environment", "e"},
		{"action", "repository_01", "tidy", "--service", "s"},
		{"action", "repository_01", "tidy", "--expected-revision", "3"},
		{"action", "repository_01", "tidy", "--confirm-action", "other"},
		{"action", "repository_01", "tidy", "--worktree"},
		{"action", "repository_01", "tidy", "--worktree", "--json"},
		{"action", "repository_01", "tidy", "--target", "demo"},
		{"action", "repository_01", "tidy", "--if-running"},
		{"actions", "repository_01"},
		{"actions", "--wait"},
		{"start", "worktree_01", "app", "--worktree", "w"},
		{"stop", ".", "--confirm-action", "tidy"},
	}
	for _, arguments := range rejected {
		if _, ok := parseArguments(arguments); ok {
			t.Fatalf("accepted %v", arguments)
		}
	}
	command, ok := parseArguments([]string{"action", "repository_01", "probe", "--environment", ".", "--service", "web", "--expected-revision", "4", "--wait"})
	if !ok || command.EnvironmentID != "." || command.ServiceID != "web" || command.ExpectedRevision == nil || *command.ExpectedRevision != 4 || !command.Wait {
		t.Fatalf("parsed: %+v ok=%v", command, ok)
	}
}

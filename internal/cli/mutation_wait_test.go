package cli

import (
	"context"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

func TestMutationVisibleRequiresTheAcceptedHealthyRun(t *testing.T) {
	receipt := contractv1.MutationReceipt{EnvironmentID: "environment_01", RunID: "run_new"}
	snapshot := contractv1.StatusSnapshot{Environments: []contractv1.Environment{{
		ID: "environment_01", DesiredState: "running", ObservedState: "running", Health: "healthy",
		Services: []contractv1.Service{{
			ID: "app", DesiredState: "running", ObservedState: "running", Health: "healthy",
			Run: &contractv1.ServiceRun{ID: "run_old"},
		}},
	}}}
	if mutationVisible(snapshot, receipt, "start", "", []string{"app"}) {
		t.Fatal("an older healthy run satisfied the accepted start")
	}
	snapshot.Environments[0].Services[0].Run.ID = "run_new"
	if !mutationVisible(snapshot, receipt, "start", "", []string{"app"}) {
		t.Fatal("the accepted healthy run was not recognized")
	}
}

func TestWaitForMutationRetriesTransientDaemonUnavailability(t *testing.T) {
	statusCalls := 0
	backend := stubBackend{readStatus: func() (contractv1.StatusSnapshot, error) {
		statusCalls++
		if statusCalls == 1 {
			return contractv1.StatusSnapshot{}, &apiclient.CodedError{Code: apiclient.ErrorDaemonUnavailable}
		}
		return contractv1.StatusSnapshot{
			Operations: []contractv1.Operation{{ID: "operation_01", State: "succeeded"}},
			Environments: []contractv1.Environment{{
				ID: "environment_01", DesiredState: "stopped", ObservedState: "stopped",
				PortLeases: []contractv1.PortLease{}, InfrastructureLeases: []contractv1.InfrastructureLease{},
			}},
		}, nil
	}}
	application := Application{Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second}
	err := application.waitForMutation(context.Background(), contractv1.MutationReceipt{
		OperationID: "operation_01", EnvironmentID: "environment_01",
	}, "stop", "", nil)
	if err != nil || statusCalls != 2 {
		t.Fatalf("err=%v status calls=%d", err, statusCalls)
	}
}

func TestWaitForMutationPreservesStructuredOperationFailure(t *testing.T) {
	backend := stubBackend{snapshot: contractv1.StatusSnapshot{Operations: []contractv1.Operation{{
		ID: "operation_01", State: "failed", Error: &contractv1.ContractError{
			Code: "WORKSPACE_NOT_READY", Message: "The workspace could not be verified.",
			Retryable: true, NextAction: "inspect_workspace_diagnostics",
		},
	}}}}
	application := Application{Backend: backend, PollInterval: time.Millisecond, WaitTimeout: time.Second}
	err := application.waitForMutation(
		context.Background(), contractv1.MutationReceipt{OperationID: "operation_01"}, "prepare", "worktree_01", nil,
	)
	contractError, ok := apiclient.ContractErrorOf(err)
	if !ok || apiclient.CodeOf(err) != apiclient.ErrorCode("WORKSPACE_NOT_READY") ||
		contractError.NextAction != "inspect_workspace_diagnostics" {
		t.Fatalf("error: %v contract=%+v", err, contractError)
	}
}

func TestWaitForMutationReportsStructuredTimeout(t *testing.T) {
	application := Application{
		Backend:      stubBackend{snapshot: contractv1.StatusSnapshot{}},
		PollInterval: time.Millisecond, WaitTimeout: 3 * time.Millisecond,
	}
	err := application.waitForMutation(
		context.Background(), contractv1.MutationReceipt{OperationID: "operation_01"}, "prepare", "worktree_01", nil,
	)
	contractError, ok := apiclient.ContractErrorOf(err)
	if !ok || apiclient.CodeOf(err) != apiclient.ErrorWaitTimeout || !contractError.Retryable ||
		contractError.ResourceKind != "operation" || contractError.ResourceID != "operation_01" ||
		contractError.NextAction != "inspect_operation_status" {
		t.Fatalf("timeout: %v contract=%+v", err, contractError)
	}
}

func TestEnvironmentStoppedRequiresEveryOwnedLeaseToDisappear(t *testing.T) {
	environment := contractv1.Environment{
		DesiredState: "stopped", ObservedState: "stopped",
		PortLeases: []contractv1.PortLease{}, InfrastructureLeases: []contractv1.InfrastructureLease{},
	}
	if !environmentStopped(environment) {
		t.Fatal("fully stopped environment was not recognized")
	}
	environment.InfrastructureLeases = []contractv1.InfrastructureLease{{ID: "infrastructure_01"}}
	if environmentStopped(environment) {
		t.Fatal("environment with an owned lease was considered stopped")
	}
}

func TestMutationVisibleRequiresPreparedWorkspaceForRequestedWorktree(t *testing.T) {
	snapshot := contractv1.StatusSnapshot{Repositories: []contractv1.Repository{{
		Worktrees: []contractv1.Worktree{
			{ID: "worktree_other", Workspace: &contractv1.WorkspaceStatus{State: "ready"}},
			{ID: "worktree_requested"},
		},
	}}}
	if mutationVisible(snapshot, contractv1.MutationReceipt{}, "prepare", "worktree_requested", nil) {
		t.Fatal("a different prepared worktree satisfied the mutation")
	}
	snapshot.Repositories[0].Worktrees[1].Workspace = &contractv1.WorkspaceStatus{State: "ready"}
	if !mutationVisible(snapshot, contractv1.MutationReceipt{}, "prepare", "worktree_requested", nil) {
		t.Fatal("the requested prepared worktree was not recognized")
	}
}

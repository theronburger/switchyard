package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/control/statusview"
)

const (
	ExitSuccess = 0
	ExitFailure = 1
	ExitUsage   = 2
)

type Backend interface {
	Status(context.Context) (contractv1.StatusSnapshot, error)
	Doctor(context.Context) apiclient.DoctorReport
	StartEnvironment(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error)
	StopEnvironment(context.Context, string, contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error)
}

type WorkspaceBackend interface {
	CreateWorktree(context.Context, contractv1.CreateWorktreeRequest) (contractv1.MutationReceipt, error)
	AdoptWorktree(context.Context, contractv1.AdoptWorktreeRequest) (contractv1.MutationReceipt, error)
	ArchiveWorktree(context.Context, contractv1.ArchiveWorktreeRequest) (contractv1.MutationReceipt, error)
}

type LiveBackend struct {
	Connector apiclient.Connector
	Now       func() time.Time
}

func (b LiveBackend) Status(ctx context.Context) (contractv1.StatusSnapshot, error) {
	return b.Connector.Status(ctx)
}

func (b LiveBackend) Doctor(ctx context.Context) apiclient.DoctorReport {
	return (apiclient.Doctor{Connector: b.Connector, Now: b.Now}).Run(ctx)
}

func (b LiveBackend) StartEnvironment(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.StartEnvironment(ctx, request)
}

func (b LiveBackend) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.StopEnvironment(ctx, environmentID, request)
}

func (b LiveBackend) CreateWorktree(
	ctx context.Context,
	request contractv1.CreateWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.CreateWorktree(ctx, request)
}

func (b LiveBackend) ArchiveWorktree(
	ctx context.Context,
	request contractv1.ArchiveWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.ArchiveWorktree(ctx, request)
}

func (b LiveBackend) AdoptWorktree(
	ctx context.Context,
	request contractv1.AdoptWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.AdoptWorktree(ctx, request)
}

type Application struct {
	Backend      Backend
	Stdout       io.Writer
	Stderr       io.Writer
	NewRequestID func() (string, error)
	Getwd        func() (string, error)
}

type parsedCommand struct {
	Name              string
	JSON              bool
	Positionals       []string
	TargetID          string
	ConfirmedTargetID string
	IdempotencyKey    string
	ExpectedRevision  *int64
	StartPoint        string
	All               bool
}

type errorOutput struct {
	SchemaVersion int                `json:"schemaVersion"`
	Error         errorOutputDetails `json:"error"`
}

type errorOutputDetails struct {
	Code           apiclient.ErrorCode `json:"code"`
	Message        string              `json:"message"`
	Retryable      bool                `json:"retryable"`
	ResourceKind   string              `json:"resourceKind,omitempty"`
	ResourceID     string              `json:"resourceId,omitempty"`
	CurrentState   string              `json:"currentState,omitempty"`
	RequestedState string              `json:"requestedState,omitempty"`
	Phase          string              `json:"phase,omitempty"`
	Step           string              `json:"step,omitempty"`
	Diagnostic     string              `json:"diagnostic,omitempty"`
	LogReference   string              `json:"logReference,omitempty"`
	NextAction     string              `json:"nextAction,omitempty"`
	ExitCode       *int                `json:"exitCode,omitempty"`
}

func (a Application) Run(ctx context.Context, arguments []string) int {
	stdout := a.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := a.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	if a.Backend == nil {
		fmt.Fprintln(stderr, "Switchyard client is not configured.")
		return ExitFailure
	}

	command, ok := parseArguments(arguments)
	if !ok {
		writeUsage(stderr)
		return ExitUsage
	}
	switch command.Name {
	case "status":
		snapshot, err := a.Backend.Status(ctx)
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		if command.All {
			if command.JSON {
				return encodeJSON(stdout, snapshot)
			}
			writeInventoryText(stdout, snapshot, "")
			return ExitSuccess
		}
		worktreeContext, fallbackPath, selectionErr := a.resolveStatusContext(snapshot, command)
		if selectionErr != nil {
			if len(command.Positionals) == 0 && errors.Is(selectionErr, statusview.ErrWorktreeNotFound) {
				if command.JSON {
					return writeStatusSelectionFailure(stdout, stderr, true, selectionErr)
				}
				writeInventoryText(stdout, snapshot, fallbackPath)
				return ExitSuccess
			}
			return writeStatusSelectionFailure(stdout, stderr, command.JSON, selectionErr)
		}
		if command.JSON {
			return encodeJSON(stdout, worktreeContext)
		}
		writeWorktreeStatusText(stdout, worktreeContext)
		return ExitSuccess
	case "doctor":
		report := a.Backend.Doctor(ctx)
		if command.JSON {
			if code := encodeJSON(stdout, report); code != ExitSuccess {
				return code
			}
		} else {
			writeDoctorText(stdout, report)
		}
		if !report.Healthy {
			return ExitFailure
		}
		return ExitSuccess
	case "start":
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := a.Backend.StartEnvironment(ctx, contractv1.StartEnvironmentRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID,
				IdempotencyKey: idempotencyKey, ExpectedEnvironmentRevision: command.ExpectedRevision,
			},
			WorktreeID: command.Positionals[0], TargetID: command.TargetID,
			ConfirmedTargetID: command.ConfirmedTargetID,
			ServiceIDs:        append([]string(nil), command.Positionals[1:]...),
		})
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		return writeReceipt(stdout, receipt, command.JSON)
	case "stop":
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := a.Backend.StopEnvironment(ctx, command.Positionals[0], contractv1.StopEnvironmentRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID,
				IdempotencyKey: idempotencyKey, ExpectedEnvironmentRevision: command.ExpectedRevision,
			},
		})
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		return writeReceipt(stdout, receipt, command.JSON)
	case "create-worktree":
		backend, available := a.Backend.(WorkspaceBackend)
		if !available {
			return writeFailure(stdout, stderr, command.JSON, fmt.Errorf("workspace actions are unavailable"))
		}
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := backend.CreateWorktree(ctx, contractv1.CreateWorktreeRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID, IdempotencyKey: idempotencyKey,
			},
			RepositoryID: command.Positionals[0], Branch: command.Positionals[1], StartPoint: command.StartPoint,
		})
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		return writeReceipt(stdout, receipt, command.JSON)
	case "archive-worktree":
		backend, available := a.Backend.(WorkspaceBackend)
		if !available {
			return writeFailure(stdout, stderr, command.JSON, fmt.Errorf("workspace actions are unavailable"))
		}
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := backend.ArchiveWorktree(ctx, contractv1.ArchiveWorktreeRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID, IdempotencyKey: idempotencyKey,
			},
			WorktreeID: command.Positionals[0],
		})
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		return writeReceipt(stdout, receipt, command.JSON)
	case "adopt-worktree":
		backend, available := a.Backend.(WorkspaceBackend)
		if !available {
			return writeFailure(stdout, stderr, command.JSON, fmt.Errorf("workspace actions are unavailable"))
		}
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := backend.AdoptWorktree(ctx, contractv1.AdoptWorktreeRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID, IdempotencyKey: idempotencyKey,
			},
			WorktreeID: command.Positionals[0],
		})
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		return writeReceipt(stdout, receipt, command.JSON)
	default:
		return ExitUsage
	}
}

func parseArguments(arguments []string) (parsedCommand, bool) {
	if len(arguments) < 1 {
		return parsedCommand{}, false
	}
	command := parsedCommand{Name: arguments[0]}
	if command.Name != "status" && command.Name != "doctor" &&
		command.Name != "start" && command.Name != "stop" &&
		command.Name != "create-worktree" && command.Name != "adopt-worktree" &&
		command.Name != "archive-worktree" {
		return parsedCommand{}, false
	}
	for index := 1; index < len(arguments); index++ {
		argument := arguments[index]
		switch argument {
		case "--json":
			if command.JSON {
				return parsedCommand{}, false
			}
			command.JSON = true
		case "--all":
			if command.All {
				return parsedCommand{}, false
			}
			command.All = true
		case "--idempotency-key":
			if command.IdempotencyKey != "" || index+1 >= len(arguments) {
				return parsedCommand{}, false
			}
			index++
			command.IdempotencyKey = arguments[index]
		case "--target":
			if command.TargetID != "" || index+1 >= len(arguments) {
				return parsedCommand{}, false
			}
			index++
			command.TargetID = arguments[index]
			if command.TargetID == "" || strings.HasPrefix(command.TargetID, "-") {
				return parsedCommand{}, false
			}
		case "--confirm-target":
			if command.ConfirmedTargetID != "" || index+1 >= len(arguments) {
				return parsedCommand{}, false
			}
			index++
			command.ConfirmedTargetID = arguments[index]
			if command.ConfirmedTargetID == "" || strings.HasPrefix(command.ConfirmedTargetID, "-") {
				return parsedCommand{}, false
			}
		case "--expected-revision":
			if command.ExpectedRevision != nil || index+1 >= len(arguments) {
				return parsedCommand{}, false
			}
			index++
			revision, err := strconv.ParseInt(arguments[index], 10, 64)
			if err != nil || revision < 0 {
				return parsedCommand{}, false
			}
			command.ExpectedRevision = &revision
		case "--base":
			if command.StartPoint != "" || index+1 >= len(arguments) {
				return parsedCommand{}, false
			}
			index++
			command.StartPoint = arguments[index]
			if command.StartPoint == "" || strings.HasPrefix(command.StartPoint, "-") {
				return parsedCommand{}, false
			}
		default:
			if strings.HasPrefix(argument, "-") {
				return parsedCommand{}, false
			}
			command.Positionals = append(command.Positionals, argument)
		}
	}
	switch command.Name {
	case "status":
		if len(command.Positionals) > 1 || (command.All && len(command.Positionals) != 0) ||
			command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.IdempotencyKey != "" || command.ExpectedRevision != nil || command.StartPoint != "" {
			return parsedCommand{}, false
		}
	case "doctor":
		if len(command.Positionals) != 0 || command.All || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.IdempotencyKey != "" || command.ExpectedRevision != nil || command.StartPoint != "" {
			return parsedCommand{}, false
		}
	case "start":
		if len(command.Positionals) < 2 || len(command.Positionals) > 33 || command.StartPoint != "" || command.All {
			return parsedCommand{}, false
		}
	case "stop":
		if len(command.Positionals) != 1 || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.StartPoint != "" || command.All {
			return parsedCommand{}, false
		}
	case "create-worktree":
		if len(command.Positionals) != 2 || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.ExpectedRevision != nil || command.All {
			return parsedCommand{}, false
		}
	case "adopt-worktree", "archive-worktree":
		if len(command.Positionals) != 1 || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.ExpectedRevision != nil || command.StartPoint != "" || command.All {
			return parsedCommand{}, false
		}
	}
	return command, true
}

func (a Application) requestID() (string, error) {
	if a.NewRequestID != nil {
		return a.NewRequestID()
	}
	contents := make([]byte, 16)
	if _, err := rand.Read(contents); err != nil {
		return "", err
	}
	return "request_" + base64.RawURLEncoding.EncodeToString(contents), nil
}

func (a Application) resolveStatusContext(
	snapshot contractv1.StatusSnapshot,
	command parsedCommand,
) (statusview.WorktreeContext, string, error) {
	if len(command.Positionals) == 1 {
		selector := command.Positionals[0]
		if selector == "." || strings.HasPrefix(selector, "."+string(filepath.Separator)) ||
			strings.HasPrefix(selector, ".."+string(filepath.Separator)) {
			workingDirectory, err := a.workingDirectory()
			if err != nil {
				return statusview.WorktreeContext{}, "", statusview.ErrInvalidSelector
			}
			selector, err = filepath.Abs(filepath.Join(workingDirectory, selector))
			if err != nil {
				return statusview.WorktreeContext{}, "", statusview.ErrInvalidSelector
			}
		}
		context, err := statusview.WorktreeBySelector(snapshot, selector)
		return context, "", err
	}
	workingDirectory, err := a.workingDirectory()
	if err != nil {
		return statusview.WorktreeContext{}, "", statusview.ErrWorktreeNotFound
	}
	context, err := statusview.WorktreeByPath(snapshot, workingDirectory)
	return context, workingDirectory, err
}

func (a Application) workingDirectory() (string, error) {
	if a.Getwd != nil {
		return a.Getwd()
	}
	return os.Getwd()
}

func writeReceipt(writer io.Writer, receipt contractv1.MutationReceipt, jsonOutput bool) int {
	if jsonOutput {
		return encodeJSON(writer, receipt)
	}
	if receipt.EnvironmentID == "" {
		fmt.Fprintf(writer, "Accepted operation %s.\n", receipt.OperationID)
	} else {
		fmt.Fprintf(writer, "Accepted operation %s for environment %s.\n", receipt.OperationID, receipt.EnvironmentID)
	}
	return ExitSuccess
}

func writeUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  switchyard status [worktree-id|branch|path] [--all] [--json]")
	fmt.Fprintln(writer, "  switchyard doctor [--json]")
	fmt.Fprintln(writer, "  switchyard start <worktree-id> <service-id>... [--target TARGET] [--confirm-target TARGET] [--expected-revision N] [--idempotency-key KEY] [--json]")
	fmt.Fprintln(writer, "  switchyard stop <environment-id> [--expected-revision N] [--idempotency-key KEY] [--json]")
	fmt.Fprintln(writer, "  switchyard create-worktree <repository-id> <branch> [--base REF] [--idempotency-key KEY] [--json]")
	fmt.Fprintln(writer, "  switchyard adopt-worktree <worktree-id> [--idempotency-key KEY] [--json]")
	fmt.Fprintln(writer, "  switchyard archive-worktree <worktree-id> [--idempotency-key KEY] [--json]")
}

func writeFailure(stdout, stderr io.Writer, jsonOutput bool, err error) int {
	code := apiclient.CodeOf(err)
	details := errorOutputDetails{Code: code, Message: "Switchyard could not complete the request."}
	if contractError, ok := apiclient.ContractErrorOf(err); ok {
		details = errorOutputDetails{
			Code: apiclient.ErrorCode(contractError.Code), Message: contractError.Message,
			Retryable:    contractError.Retryable,
			ResourceKind: contractError.ResourceKind, ResourceID: contractError.ResourceID,
			CurrentState: contractError.CurrentState, RequestedState: contractError.RequestedState,
			Phase: contractError.Phase, Step: contractError.Step, Diagnostic: contractError.Diagnostic,
			LogReference: contractError.LogReference, NextAction: contractError.NextAction,
			ExitCode: contractError.ExitCode,
		}
	}
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(errorOutput{
			SchemaVersion: contractv1.SchemaVersion,
			Error:         details,
		})
	} else {
		fmt.Fprintf(stderr, "Switchyard request failed (%s): %s\n", details.Code, details.Message)
		if details.Diagnostic != "" {
			fmt.Fprintf(stderr, "Diagnostic: %s\n", details.Diagnostic)
		}
		if details.NextAction != "" {
			fmt.Fprintf(stderr, "Next action: %s\n", details.NextAction)
		}
	}
	return ExitFailure
}

func encodeJSON(writer io.Writer, value any) int {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return ExitFailure
	}
	return ExitSuccess
}

func writeInventoryText(writer io.Writer, snapshot contractv1.StatusSnapshot, fallbackPath string) {
	if fallbackPath != "" {
		fmt.Fprintf(writer, "No known worktree contains %s; showing all environments.\n", fallbackPath)
	}
	fmt.Fprintf(writer, "Switchyard inventory revision %d\n", snapshot.SnapshotRevision)
	fmt.Fprintf(writer, "Daemon: %s (%s)\n", snapshot.Daemon.State, snapshot.Daemon.Version)
	fmt.Fprintf(writer, "Environments: %d\n", len(snapshot.Environments))

	environments := append([]contractv1.Environment(nil), snapshot.Environments...)
	sort.Slice(environments, func(left, right int) bool {
		return environments[left].DisplayName < environments[right].DisplayName
	})
	for _, environment := range environments {
		fmt.Fprintf(
			writer,
			"- %s: %s, %s, attention %d\n",
			environment.DisplayName,
			environment.ObservedState,
			environment.Health,
			len(environment.AttentionAlertIDs))
	}
}

func writeWorktreeStatusText(writer io.Writer, status statusview.WorktreeContext) {
	name := status.Worktree.Branch
	if name == "" {
		name = filepath.Base(status.Worktree.Path)
	}
	fmt.Fprintln(writer, name)
	fmt.Fprintf(writer, "Path: %s\n", status.Worktree.Path)
	fmt.Fprintf(writer, "Repository: %s (%s)\n", status.Repository.DisplayName, status.Repository.Adapter)
	ownership := "discovered"
	workspaceState := "not prepared"
	if status.Worktree.Workspace != nil {
		ownership = status.Worktree.Workspace.Ownership
		workspaceState = status.Worktree.Workspace.State
	}
	fmt.Fprintf(writer, "Workspace: %s, %s\n", ownership, workspaceState)
	fmt.Fprintf(writer, "Git: %s at %s\n", gitSummary(status.Worktree.Git), shortRevision(status.Worktree.HeadRevision))
	if status.Worktree.Changes != nil {
		fmt.Fprintf(writer, "Changes: branch +%d -%d; working tree +%d -%d\n",
			status.Worktree.Changes.Committed.Additions, status.Worktree.Changes.Committed.Deletions,
			status.Worktree.Changes.Uncommitted.Additions, status.Worktree.Changes.Uncommitted.Deletions)
	}
	if status.Worktree.PullRequest != nil && status.Worktree.PullRequest.Status == "found" &&
		status.Worktree.PullRequest.PullRequest != nil {
		pullRequest := status.Worktree.PullRequest.PullRequest
		fmt.Fprintf(writer, "Pull request: #%d %s, CI %s, review %s\n",
			pullRequest.Number, pullRequest.State, pullRequest.Checks.State, pullRequest.ReviewDecision)
	}
	if len(status.Environments) == 0 {
		fmt.Fprintln(writer, "Environment: not created")
	} else {
		for _, environment := range status.Environments {
			fmt.Fprintf(writer, "Environment: %s, %s, %s (target %s, revision %d)\n",
				environment.ID, environment.ObservedState, environment.Health, environment.TargetID, environment.Revision)
			for _, service := range environment.Services {
				url := environment.URLs[service.ID]
				if url != "" {
					fmt.Fprintf(writer, "  - %s: %s, %s, %s\n", service.DisplayName, service.ObservedState, service.Health, url)
				} else {
					fmt.Fprintf(writer, "  - %s: %s, %s\n", service.DisplayName, service.ObservedState, service.Health)
				}
			}
		}
	}
	if len(status.Operations) > 0 {
		fmt.Fprintln(writer, "Recent operations:")
		limit := min(len(status.Operations), 5)
		for _, operation := range status.Operations[:limit] {
			phase := ""
			if operation.Phase != "" {
				phase = ", " + operation.Phase
			}
			fmt.Fprintf(writer, "  - %s: %s%s (%s)\n", operation.Kind, operation.State, phase, operation.UpdatedAt.Format(time.RFC3339))
			if operation.Error != nil {
				fmt.Fprintf(writer, "    %s: %s\n", operation.Error.Code, operation.Error.Message)
				if operation.Error.Diagnostic != "" {
					fmt.Fprintf(writer, "    Diagnostic: %s\n", operation.Error.Diagnostic)
				}
			}
		}
		if len(status.Operations) > limit {
			fmt.Fprintf(writer, "  - … %d older operation(s)\n", len(status.Operations)-limit)
		}
	}
	activeAlerts := 0
	for _, alert := range status.Alerts {
		if alert.Status == "active" {
			activeAlerts++
		}
	}
	fmt.Fprintf(writer, "Attention: %d active\n", activeAlerts)
	fmt.Fprintf(writer, "Switchyard: %s, snapshot %d\n", status.Daemon.State, status.SnapshotRevision)
}

func writeStatusSelectionFailure(stdout, stderr io.Writer, jsonOutput bool, err error) int {
	code := apiclient.ErrorCode("WORKTREE_NOT_FOUND")
	message := "The requested Switchyard worktree was not found."
	if errors.Is(err, statusview.ErrWorktreeAmbiguous) {
		code = apiclient.ErrorCode("WORKTREE_AMBIGUOUS")
		message = "The worktree selector matches more than one Switchyard worktree."
	} else if errors.Is(err, statusview.ErrInvalidSelector) {
		code = apiclient.ErrorCode("WORKTREE_SELECTOR_INVALID")
		message = "The worktree selector is invalid."
	}
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(errorOutput{
			SchemaVersion: contractv1.SchemaVersion,
			Error:         errorOutputDetails{Code: code, Message: message},
		})
	} else {
		fmt.Fprintln(stderr, message)
	}
	return ExitFailure
}

func gitSummary(state contractv1.WorktreeState) string {
	parts := make([]string, 0, 4)
	if state.HasTrackedChanges {
		parts = append(parts, "tracked changes")
	}
	if state.HasUntrackedFiles {
		parts = append(parts, "untracked files")
	}
	if state.HasUnpushedCommits {
		parts = append(parts, "unpushed commits")
	}
	if state.Locked {
		parts = append(parts, "locked")
	}
	if len(parts) == 0 {
		return "clean"
	}
	return strings.Join(parts, ", ")
}

func shortRevision(revision string) string {
	if len(revision) > 8 {
		return revision[:8]
	}
	if revision == "" {
		return "unknown revision"
	}
	return revision
}

func writeDoctorText(writer io.Writer, report apiclient.DoctorReport) {
	for _, check := range report.Checks {
		symbol := "-"
		switch check.Status {
		case apiclient.CheckPass:
			symbol = "✓"
		case apiclient.CheckFail:
			symbol = "✗"
		}
		if check.ErrorCode != "" {
			fmt.Fprintf(writer, "%s %s: %s (%s)\n", symbol, check.ID, check.Summary, check.ErrorCode)
		} else {
			fmt.Fprintf(writer, "%s %s: %s\n", symbol, check.ID, check.Summary)
		}
	}
}

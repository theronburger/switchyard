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
	PrepareWorktree(context.Context, contractv1.PrepareWorktreeRequest) (contractv1.MutationReceipt, error)
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

func (b LiveBackend) PrepareWorktree(
	ctx context.Context,
	request contractv1.PrepareWorktreeRequest,
) (contractv1.MutationReceipt, error) {
	return b.Connector.PrepareWorktree(ctx, request)
}

type Application struct {
	Backend      Backend
	Stdout       io.Writer
	Stderr       io.Writer
	NewRequestID func() (string, error)
	Getwd        func() (string, error)
	PollInterval time.Duration
	WaitTimeout  time.Duration
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
	Wait              bool
	IfRunning         bool
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

type noRunningEnvironmentOutput struct {
	SchemaVersion int    `json:"schemaVersion"`
	Outcome       string `json:"outcome"`
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
		_, _ = fmt.Fprintln(stderr, "Switchyard client is not configured.")
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
		actionContext, cancel := a.actionContext(ctx, command.Wait)
		defer cancel()
		selector := command.Positionals[0]
		worktreeID := command.Positionals[0]
		if worktreeID == "." {
			resolvedID, err := a.resolveCurrentWorktree(actionContext, command.Wait)
			if err != nil {
				return writeCurrentWorktreeFailure(stdout, stderr, command.JSON, err)
			}
			worktreeID = resolvedID
		}
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := a.submitStart(actionContext, contractv1.StartEnvironmentRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID,
				IdempotencyKey: idempotencyKey, ExpectedEnvironmentRevision: command.ExpectedRevision,
			},
			WorktreeID: worktreeID, TargetID: command.TargetID,
			ConfirmedTargetID: command.ConfirmedTargetID,
			ServiceIDs:        append([]string(nil), command.Positionals[1:]...),
		}, command.Wait && selector == ".")
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		if command.Wait {
			if err := a.waitForMutation(actionContext, receipt, "start", "", command.Positionals[1:]); err != nil {
				return writeFailure(stdout, stderr, command.JSON, err)
			}
		}
		return writeReceipt(stdout, receipt, command.JSON)
	case "stop":
		actionContext, cancel := a.actionContext(ctx, command.Wait)
		defer cancel()
		environmentID := command.Positionals[0]
		if environmentID == "." {
			environment, err := a.resolveCurrentEnvironment(actionContext, command.Wait)
			if err != nil {
				if command.IfRunning && errors.Is(err, statusview.ErrEnvironmentNotFound) {
					return writeNoRunningEnvironment(stdout, command.JSON)
				}
				if apiclient.CodeOf(err) != apiclient.ErrorUnknown {
					return writeFailure(stdout, stderr, command.JSON, err)
				}
				return writeStatusSelectionFailure(stdout, stderr, command.JSON, err)
			}
			if command.IfRunning && environmentStopped(environment) {
				return writeNoRunningEnvironment(stdout, command.JSON)
			}
			environmentID = environment.ID
		}
		requestID, err := a.requestID()
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		idempotencyKey := command.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = "cli:" + requestID
		}
		receipt, err := a.submitStop(actionContext, environmentID, contractv1.StopEnvironmentRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: requestID,
				IdempotencyKey: idempotencyKey, ExpectedEnvironmentRevision: command.ExpectedRevision,
			},
		}, command.Wait)
		if err != nil {
			return writeFailure(stdout, stderr, command.JSON, err)
		}
		if command.Wait {
			if err := a.waitForMutation(actionContext, receipt, "stop", "", nil); err != nil {
				return writeFailure(stdout, stderr, command.JSON, err)
			}
		}
		return writeReceipt(stdout, receipt, command.JSON)
	case "prepare":
		backend, available := a.Backend.(WorkspaceBackend)
		if !available {
			return writeFailure(stdout, stderr, command.JSON, fmt.Errorf("workspace actions are unavailable"))
		}
		actionContext, cancel := a.actionContext(ctx, command.Wait)
		defer cancel()
		worktreeID := command.Positionals[0]
		if worktreeID == "." {
			resolvedID, err := a.resolveCurrentWorktree(actionContext, command.Wait)
			if err != nil {
				return writeCurrentWorktreeFailure(stdout, stderr, command.JSON, err)
			}
			worktreeID = resolvedID
		}
		for {
			requestID, err := a.requestID()
			if err != nil {
				return writeFailure(stdout, stderr, command.JSON, err)
			}
			idempotencyKey := command.IdempotencyKey
			if idempotencyKey == "" {
				idempotencyKey = "cli:" + requestID
			}
			receipt, err := a.submitPrepare(actionContext, backend, contractv1.PrepareWorktreeRequest{
				MutationRequest: contractv1.MutationRequest{
					SchemaVersion: contractv1.SchemaVersion, RequestID: requestID, IdempotencyKey: idempotencyKey,
				},
				WorktreeID: worktreeID,
			}, command.Wait)
			if err != nil {
				return writeFailure(stdout, stderr, command.JSON, err)
			}
			if !command.Wait {
				return writeReceipt(stdout, receipt, command.JSON)
			}
			if err := a.waitForMutation(actionContext, receipt, "prepare", worktreeID, nil); err != nil {
				if command.IdempotencyKey == "" && retryableInterruptedPreparation(err) && actionContext.Err() == nil {
					continue
				}
				return writeFailure(stdout, stderr, command.JSON, err)
			}
			return writeReceipt(stdout, receipt, command.JSON)
		}
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

func (a Application) resolveCurrentWorktree(ctx context.Context, wait bool) (string, error) {
	if !wait {
		snapshot, err := a.Backend.Status(ctx)
		if err != nil {
			return "", err
		}
		worktreeContext, _, err := a.resolveStatusContext(snapshot, parsedCommand{Name: "status"})
		if err != nil {
			return "", err
		}
		return worktreeContext.Worktree.ID, nil
	}
	waitContext, cancel := context.WithTimeout(ctx, a.waitTimeout())
	defer cancel()
	var lastErr error
	for {
		snapshot, err := a.Backend.Status(waitContext)
		if err == nil {
			worktreeContext, _, selectionErr := a.resolveStatusContext(snapshot, parsedCommand{Name: "status"})
			if selectionErr == nil {
				return worktreeContext.Worktree.ID, nil
			}
			if !errors.Is(selectionErr, statusview.ErrWorktreeNotFound) {
				return "", selectionErr
			}
			lastErr = selectionErr
		} else if retryableDaemonDiscoveryError(err) {
			lastErr = err
		} else {
			return "", err
		}
		if err := a.waitForNextPoll(waitContext); err != nil {
			if lastErr != nil {
				return "", lastErr
			}
			return "", err
		}
	}
}

func (a Application) submitStart(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
	retryDiscovery bool,
) (contractv1.MutationReceipt, error) {
	for {
		receipt, err := a.Backend.StartEnvironment(ctx, request)
		if err == nil || !retryDiscovery ||
			(!retryableDaemonDiscoveryError(err) && apiclient.CodeOf(err) != apiclient.ErrorCode("WORKTREE_NOT_FOUND")) {
			return receipt, err
		}
		if waitErr := a.waitForNextPoll(ctx); waitErr != nil {
			return contractv1.MutationReceipt{}, err
		}
	}
}

func (a Application) resolveCurrentEnvironment(
	ctx context.Context,
	retryDiscovery bool,
) (contractv1.Environment, error) {
	var lastErr error
	for {
		snapshot, err := a.Backend.Status(ctx)
		if err == nil {
			worktreeContext, _, selectionErr := a.resolveStatusContext(snapshot, parsedCommand{Name: "status"})
			switch {
			case selectionErr == nil && len(worktreeContext.Environments) == 1:
				return worktreeContext.Environments[0], nil
			case selectionErr == nil && len(worktreeContext.Environments) == 0:
				return contractv1.Environment{}, statusview.ErrEnvironmentNotFound
			case selectionErr == nil:
				return contractv1.Environment{}, statusview.ErrWorktreeAmbiguous
			case retryDiscovery && errors.Is(selectionErr, statusview.ErrWorktreeNotFound):
				lastErr = selectionErr
			default:
				return contractv1.Environment{}, selectionErr
			}
		} else if retryDiscovery && retryableDaemonDiscoveryError(err) {
			lastErr = err
		} else {
			return contractv1.Environment{}, err
		}
		if waitErr := a.waitForNextPoll(ctx); waitErr != nil {
			if lastErr != nil {
				return contractv1.Environment{}, lastErr
			}
			return contractv1.Environment{}, waitErr
		}
	}
}

func (a Application) submitStop(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
	retryDiscovery bool,
) (contractv1.MutationReceipt, error) {
	for {
		receipt, err := a.Backend.StopEnvironment(ctx, environmentID, request)
		if err == nil || !retryDiscovery || !retryableDaemonDiscoveryError(err) {
			return receipt, err
		}
		if waitErr := a.waitForNextPoll(ctx); waitErr != nil {
			return contractv1.MutationReceipt{}, err
		}
	}
}

func (a Application) submitPrepare(
	ctx context.Context,
	backend WorkspaceBackend,
	request contractv1.PrepareWorktreeRequest,
	wait bool,
) (contractv1.MutationReceipt, error) {
	if !wait {
		return backend.PrepareWorktree(ctx, request)
	}
	waitContext, cancel := context.WithTimeout(ctx, a.waitTimeout())
	defer cancel()
	for {
		receipt, err := backend.PrepareWorktree(waitContext, request)
		if err == nil {
			return receipt, nil
		}
		if !retryableDaemonDiscoveryError(err) && apiclient.CodeOf(err) != apiclient.ErrorCode("WORKTREE_NOT_FOUND") {
			return contractv1.MutationReceipt{}, err
		}
		if waitErr := a.waitForNextPoll(waitContext); waitErr != nil {
			return contractv1.MutationReceipt{}, err
		}
	}
}

func retryableDaemonDiscoveryError(err error) bool {
	switch apiclient.CodeOf(err) {
	case apiclient.ErrorDaemonUnavailable,
		apiclient.ErrorRuntimeDescriptorUnavailable,
		apiclient.ErrorRuntimeDescriptorStale:
		return true
	default:
		return false
	}
}

func retryableInterruptedPreparation(err error) bool {
	switch apiclient.CodeOf(err) {
	case apiclient.ErrorCode("DAEMON_RESTARTED"), apiclient.ErrorCode("WORKSPACE_ACTION_INTERRUPTED"):
		return true
	default:
		return false
	}
}

func (a Application) actionContext(ctx context.Context, wait bool) (context.Context, context.CancelFunc) {
	if !wait {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, a.waitTimeout())
}

func (a Application) waitTimeout() time.Duration {
	if a.WaitTimeout > 0 {
		return a.WaitTimeout
	}
	return defaultWaitTimeout
}

func parseArguments(arguments []string) (parsedCommand, bool) {
	if len(arguments) < 1 {
		return parsedCommand{}, false
	}
	command := parsedCommand{Name: arguments[0]}
	if command.Name != "status" && command.Name != "doctor" &&
		command.Name != "start" && command.Name != "stop" && command.Name != "prepare" &&
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
		case "--wait":
			if command.Wait {
				return parsedCommand{}, false
			}
			command.Wait = true
		case "--if-running":
			if command.IfRunning {
				return parsedCommand{}, false
			}
			command.IfRunning = true
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
			command.IdempotencyKey != "" || command.ExpectedRevision != nil || command.StartPoint != "" ||
			command.Wait || command.IfRunning {
			return parsedCommand{}, false
		}
	case "doctor":
		if len(command.Positionals) != 0 || command.All || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.IdempotencyKey != "" || command.ExpectedRevision != nil || command.StartPoint != "" ||
			command.Wait || command.IfRunning {
			return parsedCommand{}, false
		}
	case "start":
		if len(command.Positionals) < 2 || len(command.Positionals) > 33 || command.StartPoint != "" || command.All ||
			command.IfRunning {
			return parsedCommand{}, false
		}
	case "stop":
		if len(command.Positionals) != 1 || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.StartPoint != "" || command.All || (command.IfRunning && command.Positionals[0] != ".") {
			return parsedCommand{}, false
		}
	case "prepare":
		if len(command.Positionals) != 1 || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.ExpectedRevision != nil || command.StartPoint != "" || command.All || command.IfRunning {
			return parsedCommand{}, false
		}
	case "create-worktree":
		if len(command.Positionals) != 2 || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.ExpectedRevision != nil || command.All || command.Wait || command.IfRunning {
			return parsedCommand{}, false
		}
	case "adopt-worktree", "archive-worktree":
		if len(command.Positionals) != 1 || command.TargetID != "" || command.ConfirmedTargetID != "" ||
			command.ExpectedRevision != nil || command.StartPoint != "" || command.All || command.Wait || command.IfRunning {
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
		_, _ = fmt.Fprintf(writer, "Accepted operation %s.\n", receipt.OperationID)
	} else {
		_, _ = fmt.Fprintf(writer, "Accepted operation %s for environment %s.\n", receipt.OperationID, receipt.EnvironmentID)
	}
	return ExitSuccess
}

func writeUsage(writer io.Writer) {
	_, _ = fmt.Fprintln(writer, "usage:")
	_, _ = fmt.Fprintln(writer, "  switchyard status [worktree-id|branch|path] [--all] [--json]")
	_, _ = fmt.Fprintln(writer, "  switchyard doctor [--json]")
	_, _ = fmt.Fprintln(writer, "  switchyard start <worktree-id|.> <service-id>... [--target TARGET] [--confirm-target TARGET] [--expected-revision N] [--idempotency-key KEY] [--wait] [--json]")
	_, _ = fmt.Fprintln(writer, "  switchyard stop <environment-id|.> [--expected-revision N] [--idempotency-key KEY] [--if-running] [--wait] [--json]")
	_, _ = fmt.Fprintln(writer, "  switchyard prepare <worktree-id|.> [--idempotency-key KEY] [--wait] [--json]")
	_, _ = fmt.Fprintln(writer, "  switchyard create-worktree <repository-id> <branch> [--base REF] [--idempotency-key KEY] [--json]")
	_, _ = fmt.Fprintln(writer, "  switchyard adopt-worktree <worktree-id> [--idempotency-key KEY] [--json]")
	_, _ = fmt.Fprintln(writer, "  switchyard archive-worktree <worktree-id> [--idempotency-key KEY] [--json]")
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
		_, _ = fmt.Fprintf(stderr, "Switchyard request failed (%s): %s\n", details.Code, details.Message)
		if details.Diagnostic != "" {
			_, _ = fmt.Fprintf(stderr, "Diagnostic: %s\n", details.Diagnostic)
		}
		if details.NextAction != "" {
			_, _ = fmt.Fprintf(stderr, "Next action: %s\n", details.NextAction)
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
		_, _ = fmt.Fprintf(writer, "No known worktree contains %s; showing all environments.\n", fallbackPath)
	}
	_, _ = fmt.Fprintf(writer, "Switchyard inventory revision %d\n", snapshot.SnapshotRevision)
	_, _ = fmt.Fprintf(writer, "Daemon: %s (%s)\n", snapshot.Daemon.State, snapshot.Daemon.Version)
	_, _ = fmt.Fprintf(writer, "Environments: %d\n", len(snapshot.Environments))

	environments := append([]contractv1.Environment(nil), snapshot.Environments...)
	sort.Slice(environments, func(left, right int) bool {
		return environments[left].DisplayName < environments[right].DisplayName
	})
	for _, environment := range environments {
		_, _ = fmt.Fprintf(
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
	_, _ = fmt.Fprintln(writer, name)
	_, _ = fmt.Fprintf(writer, "Path: %s\n", status.Worktree.Path)
	_, _ = fmt.Fprintf(writer, "Repository: %s (%s)\n", status.Repository.DisplayName, status.Repository.Adapter)
	ownership := "discovered"
	workspaceState := "not prepared"
	if status.Worktree.Workspace != nil {
		ownership = status.Worktree.Workspace.Ownership
		workspaceState = status.Worktree.Workspace.State
	}
	_, _ = fmt.Fprintf(writer, "Workspace: %s, %s\n", ownership, workspaceState)
	_, _ = fmt.Fprintf(writer, "Git: %s at %s\n", gitSummary(status.Worktree.Git), shortRevision(status.Worktree.HeadRevision))
	if status.Worktree.Changes != nil {
		_, _ = fmt.Fprintf(writer, "Changes: branch +%d -%d; working tree +%d -%d\n",
			status.Worktree.Changes.Committed.Additions, status.Worktree.Changes.Committed.Deletions,
			status.Worktree.Changes.Uncommitted.Additions, status.Worktree.Changes.Uncommitted.Deletions)
	}
	if status.Worktree.PullRequest != nil && status.Worktree.PullRequest.Status == "found" &&
		status.Worktree.PullRequest.PullRequest != nil {
		pullRequest := status.Worktree.PullRequest.PullRequest
		_, _ = fmt.Fprintf(writer, "Pull request: #%d %s, CI %s, review %s\n",
			pullRequest.Number, pullRequest.State, pullRequest.Checks.State, pullRequest.ReviewDecision)
	}
	if len(status.Environments) == 0 {
		_, _ = fmt.Fprintln(writer, "Environment: not created")
	} else {
		for _, environment := range status.Environments {
			_, _ = fmt.Fprintf(writer, "Environment: %s, %s, %s (target %s, revision %d)\n",
				environment.ID, environment.ObservedState, environment.Health, environment.TargetID, environment.Revision)
			for _, service := range environment.Services {
				url := environment.URLs[service.ID]
				if url != "" {
					_, _ = fmt.Fprintf(writer, "  - %s: %s, %s, %s\n", service.DisplayName, service.ObservedState, service.Health, url)
				} else {
					_, _ = fmt.Fprintf(writer, "  - %s: %s, %s\n", service.DisplayName, service.ObservedState, service.Health)
				}
			}
		}
	}
	if len(status.Operations) > 0 {
		_, _ = fmt.Fprintln(writer, "Recent operations:")
		limit := min(len(status.Operations), 5)
		for _, operation := range status.Operations[:limit] {
			phase := ""
			if operation.Phase != "" {
				phase = ", " + operation.Phase
			}
			_, _ = fmt.Fprintf(writer, "  - %s: %s%s (%s)\n", operation.Kind, operation.State, phase, operation.UpdatedAt.Format(time.RFC3339))
			if operation.Error != nil {
				_, _ = fmt.Fprintf(writer, "    %s: %s\n", operation.Error.Code, operation.Error.Message)
				if operation.Error.Diagnostic != "" {
					_, _ = fmt.Fprintf(writer, "    Diagnostic: %s\n", operation.Error.Diagnostic)
				}
			}
		}
		if len(status.Operations) > limit {
			_, _ = fmt.Fprintf(writer, "  - … %d older operation(s)\n", len(status.Operations)-limit)
		}
	}
	activeAlerts := 0
	for _, alert := range status.Alerts {
		if alert.Status == "active" {
			activeAlerts++
		}
	}
	_, _ = fmt.Fprintf(writer, "Attention: %d active\n", activeAlerts)
	_, _ = fmt.Fprintf(writer, "Switchyard: %s, snapshot %d\n", status.Daemon.State, status.SnapshotRevision)
}

func writeStatusSelectionFailure(stdout, stderr io.Writer, jsonOutput bool, err error) int {
	code := apiclient.ErrorCode("WORKTREE_NOT_FOUND")
	message := "The requested Switchyard worktree was not found."
	if errors.Is(err, statusview.ErrEnvironmentNotFound) {
		code = apiclient.ErrorCode("ENVIRONMENT_NOT_FOUND")
		message = "This worktree does not have a Switchyard environment."
	} else if errors.Is(err, statusview.ErrWorktreeAmbiguous) {
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
		_, _ = fmt.Fprintln(stderr, message)
	}
	return ExitFailure
}

func writeNoRunningEnvironment(writer io.Writer, jsonOutput bool) int {
	if jsonOutput {
		return encodeJSON(writer, noRunningEnvironmentOutput{
			SchemaVersion: contractv1.SchemaVersion, Outcome: "alreadyStopped",
		})
	}
	_, _ = fmt.Fprintln(writer, "No Switchyard environment is running for this worktree.")
	return ExitSuccess
}

func writeCurrentWorktreeFailure(stdout, stderr io.Writer, jsonOutput bool, err error) int {
	if errors.Is(err, statusview.ErrWorktreeNotFound) || errors.Is(err, statusview.ErrWorktreeAmbiguous) ||
		errors.Is(err, statusview.ErrInvalidSelector) {
		return writeStatusSelectionFailure(stdout, stderr, jsonOutput, err)
	}
	return writeFailure(stdout, stderr, jsonOutput, err)
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
			_, _ = fmt.Fprintf(writer, "%s %s: %s (%s)\n", symbol, check.ID, check.Summary, check.ErrorCode)
		} else {
			_, _ = fmt.Fprintf(writer, "%s %s: %s\n", symbol, check.ID, check.Summary)
		}
	}
}

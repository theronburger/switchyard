package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/control/statusview"
)

const (
	ProtocolVersion               = "2026-07-28"
	LegacyProtocolVersion         = "2025-11-25"
	LegacyPreviousProtocolVersion = "2025-06-18"
	maximumMessage                = 1024 * 1024

	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
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

type DiagnosticsBackend interface {
	OperationDiagnostics(context.Context, string, int) (contractv1.OperationDiagnostics, error)
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

func (b LiveBackend) OperationDiagnostics(
	ctx context.Context,
	operationID string,
	maximumBytes int,
) (contractv1.OperationDiagnostics, error) {
	return b.Connector.OperationDiagnostics(ctx, operationID, maximumBytes)
}

type Server struct {
	Backend Backend
	Name    string
	Version string
}

type request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *responseError   `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ClientInfo      json.RawMessage `json:"clientInfo"`
}

type initializeResult struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]any         `json:"capabilities"`
	ServerInfo      implementationMetadata `json:"serverInfo"`
}

type modernResultFields struct {
	ResultType string         `json:"resultType,omitempty"`
	Meta       map[string]any `json:"_meta,omitempty"`
}

type discoverResult struct {
	modernResultFields
	SupportedVersions []string       `json:"supportedVersions"`
	Capabilities      map[string]any `json:"capabilities"`
	Instructions      string         `json:"instructions,omitempty"`
	TTLMilliseconds   int            `json:"ttlMs"`
	CacheScope        string         `json:"cacheScope"`
}

type listToolsResult struct {
	modernResultFields
	Tools           []toolDefinition `json:"tools"`
	TTLMilliseconds int              `json:"ttlMs"`
	CacheScope      string           `json:"cacheScope"`
}

type implementationMetadata struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema map[string]any  `json:"inputSchema"`
	Annotations toolAnnotations `json:"annotations"`
}

type toolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

type callToolParams struct {
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	Meta           json.RawMessage `json:"_meta,omitempty"`
	InputResponses json.RawMessage `json:"inputResponses,omitempty"`
	RequestState   json.RawMessage `json:"requestState,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callToolResult struct {
	modernResultFields
	Content           []textContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

type contextArguments struct {
	WorktreePath string `json:"worktreePath"`
}

type environmentStatusArguments struct {
	EnvironmentID string `json:"environmentId"`
}

type operationDiagnosticsArguments struct {
	OperationID string `json:"operationId"`
	MaxBytes    int    `json:"maxBytes,omitempty"`
}

type worktreeContextOutput struct {
	Context statusview.WorktreeContext `json:"context"`
}

type environmentStatusOutput struct {
	Status statusview.EnvironmentStatus `json:"status"`
}

type inventoryOutput struct {
	Inventory contractv1.StatusSnapshot `json:"inventory"`
}

type doctorOutput struct {
	Doctor apiclient.DoctorReport `json:"doctor"`
}

type operationDiagnosticsOutput struct {
	Diagnostics contractv1.OperationDiagnostics `json:"diagnostics"`
}

type startArguments struct {
	RequestID                   string   `json:"requestId"`
	IdempotencyKey              string   `json:"idempotencyKey"`
	WorktreeID                  string   `json:"worktreeId"`
	TargetID                    string   `json:"targetId,omitempty"`
	ConfirmedTargetID           string   `json:"confirmedTargetId,omitempty"`
	ServiceIDs                  []string `json:"serviceIds"`
	ExpectedEnvironmentRevision *int64   `json:"expectedEnvironmentRevision,omitempty"`
}

type stopArguments struct {
	RequestID                   string `json:"requestId"`
	IdempotencyKey              string `json:"idempotencyKey"`
	EnvironmentID               string `json:"environmentId"`
	ExpectedEnvironmentRevision *int64 `json:"expectedEnvironmentRevision,omitempty"`
}

type createWorktreeArguments struct {
	RequestID      string `json:"requestId"`
	IdempotencyKey string `json:"idempotencyKey"`
	RepositoryID   string `json:"repositoryId"`
	Branch         string `json:"branch"`
	StartPoint     string `json:"startPoint,omitempty"`
}

type archiveWorktreeArguments struct {
	RequestID      string `json:"requestId"`
	IdempotencyKey string `json:"idempotencyKey"`
	WorktreeID     string `json:"worktreeId"`
}

type mutationOutput struct {
	Receipt            contractv1.MutationReceipt     `json:"receipt"`
	EnvironmentContext *contractv1.EnvironmentContext `json:"environmentContext,omitempty"`
}

type toolErrorOutput struct {
	Error toolErrorDetails `json:"error"`
}

type toolErrorDetails struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Retryable      bool   `json:"retryable"`
	ResourceKind   string `json:"resourceKind,omitempty"`
	ResourceID     string `json:"resourceId,omitempty"`
	CurrentState   string `json:"currentState,omitempty"`
	RequestedState string `json:"requestedState,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Step           string `json:"step,omitempty"`
	Diagnostic     string `json:"diagnostic,omitempty"`
	LogReference   string `json:"logReference,omitempty"`
	NextAction     string `json:"nextAction,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
}

type sessionState struct {
	legacyInitializeAccepted bool
	legacyReady              bool
}

func (s Server) Run(ctx context.Context, input io.Reader, output io.Writer) error {
	if s.Backend == nil {
		return fmt.Errorf("MCP backend is required")
	}
	if s.Name == "" {
		s.Name = "switchyard"
	}
	if s.Version == "" {
		s.Version = "development"
	}

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maximumMessage)
	encoder := json.NewEncoder(output)
	state := sessionState{}
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		var incoming request
		if err := json.Unmarshal(line, &incoming); err != nil {
			if err := encoder.Encode(protocolError(nil, -32700, "Parse error")); err != nil {
				return err
			}
			continue
		}
		outgoing, shouldRespond := s.handle(ctx, incoming, &state)
		if shouldRespond {
			if err := encoder.Encode(outgoing); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (s Server) handle(ctx context.Context, incoming request, state *sessionState) (response, bool) {
	if incoming.JSONRPC != "2.0" || incoming.Method == "" {
		if incoming.ID == nil {
			return response{}, false
		}
		return protocolError(incoming.ID, -32600, "Invalid request"), true
	}

	switch incoming.Method {
	case "initialize":
		modern, metadataFailure := validateModernMetadata(incoming.Params)
		if metadataFailure != nil {
			if incoming.ID == nil {
				return response{}, false
			}
			return protocolErrorWithData(
				incoming.ID,
				metadataFailure.Code,
				metadataFailure.Message,
				metadataFailure.Data), true
		}
		if modern {
			if incoming.ID == nil {
				return response{}, false
			}
			return protocolError(incoming.ID, -32601, "Method not found"), true
		}
		if incoming.ID == nil || state.legacyInitializeAccepted {
			return protocolError(incoming.ID, -32600, "Invalid initialize request"), incoming.ID != nil
		}
		var params initializeParams
		if err := decodePermissiveParams(incoming.Params, &params); err != nil ||
			params.ProtocolVersion == "" || !validJSONObject(params.Capabilities) ||
			!validImplementation(params.ClientInfo) {
			return protocolError(incoming.ID, -32602, "Invalid initialize parameters"), true
		}
		state.legacyInitializeAccepted = true
		return success(incoming.ID, initializeResult{
			ProtocolVersion: negotiateLegacyProtocolVersion(params.ProtocolVersion),
			Capabilities:    map[string]any{"tools": map[string]any{}},
			ServerInfo:      implementationMetadata{Name: s.Name, Version: s.Version},
		}), true
	case "notifications/initialized":
		if state.legacyInitializeAccepted {
			state.legacyReady = true
		}
		return response{}, false
	}

	modern, metadataFailure := validateModernMetadata(incoming.Params)
	if metadataFailure != nil {
		if incoming.ID == nil {
			return response{}, false
		}
		return protocolErrorWithData(
			incoming.ID,
			metadataFailure.Code,
			metadataFailure.Message,
			metadataFailure.Data), true
	}
	if incoming.Method == "server/discover" {
		if incoming.ID == nil {
			return response{}, false
		}
		if !modern {
			return protocolError(incoming.ID, -32602, "Modern request metadata is required"), true
		}
		return success(incoming.ID, discoverResult{
			modernResultFields: s.modernResultFields(),
			SupportedVersions:  []string{ProtocolVersion},
			Capabilities:       map[string]any{"tools": map[string]any{}},
			Instructions:       "Call switchyard_context with the absolute active workspace path before acting. Use switchyard_inventory only for global discovery, submit idempotent local environment actions, and request switchyard_operation_diagnostics only when a failed operation's bounded structured diagnostic is insufficient.",
			TTLMilliseconds:    0,
			CacheScope:         "public",
		}), true
	}

	if !modern && !state.legacyReady {
		if incoming.ID == nil {
			return response{}, false
		}
		return protocolError(incoming.ID, -32600, "Server is not initialized"), true
	}
	if incoming.ID == nil {
		return response{}, false
	}

	switch incoming.Method {
	case "ping":
		if modern {
			return protocolError(incoming.ID, -32601, "Method not found"), true
		}
		return success(incoming.ID, map[string]any{}), true
	case "tools/list":
		if modern {
			return success(incoming.ID, listToolsResult{
				modernResultFields: s.modernResultFields(),
				Tools:              toolDefinitions(),
				TTLMilliseconds:    0,
				CacheScope:         "public",
			}), true
		}
		return success(incoming.ID, map[string]any{"tools": toolDefinitions()}), true
	case "tools/call":
		result, protocolFailure := s.callTool(ctx, incoming.Params, modern)
		if protocolFailure != nil {
			return protocolError(incoming.ID, protocolFailure.Code, protocolFailure.Message), true
		}
		return success(incoming.ID, result), true
	default:
		return protocolError(incoming.ID, -32601, "Method not found"), true
	}
}

func (s Server) callTool(
	ctx context.Context,
	rawParams json.RawMessage,
	modern bool,
) (callToolResult, *responseError) {
	var params callToolParams
	if err := decodeParams(rawParams, &params); err != nil || params.Name == "" {
		return callToolResult{}, &responseError{Code: -32602, Message: "Invalid tool call parameters"}
	}

	switch params.Name {
	case "switchyard_context":
		var arguments contextArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil || arguments.WorktreePath == "" {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid worktree context arguments"}
		}
		snapshot, err := s.Backend.Status(ctx)
		if err != nil {
			return s.decorateCallResult(daemonToolError(err), modern), nil
		}
		contextView, err := statusview.WorktreeByPath(snapshot, arguments.WorktreePath)
		if err != nil {
			return s.decorateCallResult(callToolResult{
				Content: []textContent{{Type: "text", Text: "The absolute workspace path did not resolve to exactly one Switchyard worktree."}},
				StructuredContent: toolErrorOutput{Error: toolErrorDetails{
					Code:    "WORKTREE_NOT_FOUND",
					Message: "The absolute workspace path did not resolve to exactly one Switchyard worktree.",
				}},
				IsError: true,
			}, modern), nil
		}
		state := "has no environment"
		if len(contextView.Environments) == 1 {
			state = contextView.Environments[0].ObservedState + "/" + contextView.Environments[0].Health
		} else if len(contextView.Environments) > 1 {
			state = fmt.Sprintf("has %d environments", len(contextView.Environments))
		}
		return s.decorateCallResult(callToolResult{
			Content: []textContent{{
				Type: "text",
				Text: fmt.Sprintf("Worktree %s %s at Switchyard snapshot %d.%s",
					contextView.Worktree.Branch, state, snapshot.SnapshotRevision,
					repositoryObservationSummary(contextView.Repository.Observation)),
			}},
			StructuredContent: worktreeContextOutput{Context: contextView},
		}, modern), nil
	case "switchyard_environment_status":
		var arguments environmentStatusArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil || arguments.EnvironmentID == "" {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid environment status arguments"}
		}
		snapshot, err := s.Backend.Status(ctx)
		if err != nil {
			return s.decorateCallResult(daemonToolError(err), modern), nil
		}
		environmentStatus, err := statusview.EnvironmentByID(snapshot, arguments.EnvironmentID)
		if err != nil {
			return s.decorateCallResult(callToolResult{
				Content: []textContent{{Type: "text", Text: "The requested Switchyard environment was not found."}},
				StructuredContent: toolErrorOutput{Error: toolErrorDetails{
					Code: "ENVIRONMENT_NOT_FOUND", Message: "The requested Switchyard environment was not found.",
				}},
				IsError: true,
			}, modern), nil
		}
		return s.decorateCallResult(callToolResult{
			Content:           []textContent{{Type: "text", Text: environmentStatusSummary(environmentStatus)}},
			StructuredContent: environmentStatusOutput{Status: environmentStatus},
		}, modern), nil
	case "switchyard_inventory":
		var arguments struct{}
		if err := decodeOptionalObject(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid inventory arguments"}
		}
		snapshot, err := s.Backend.Status(ctx)
		if err != nil {
			return s.decorateCallResult(daemonToolError(err), modern), nil
		}
		return s.decorateCallResult(callToolResult{
			Content: []textContent{{Type: "text", Text: fmt.Sprintf(
				"Switchyard inventory snapshot %d contains %d repositories, %d worktrees, and %d environments.",
				snapshot.SnapshotRevision, len(snapshot.Repositories), worktreeCount(snapshot.Repositories), len(snapshot.Environments))}},
			StructuredContent: inventoryOutput{Inventory: snapshot},
		}, modern), nil
	case "switchyard_operation_diagnostics":
		var arguments operationDiagnosticsArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil || arguments.OperationID == "" ||
			(arguments.MaxBytes != 0 && (arguments.MaxBytes < 256 || arguments.MaxBytes > 32*1024)) {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid operation diagnostics arguments"}
		}
		backend, available := s.Backend.(DiagnosticsBackend)
		if !available {
			return s.decorateCallResult(callToolResult{
				Content: []textContent{{Type: "text", Text: "Switchyard operation diagnostics are unavailable."}},
				StructuredContent: toolErrorOutput{Error: toolErrorDetails{
					Code: "DIAGNOSTICS_UNAVAILABLE", Message: "Switchyard operation diagnostics are unavailable.", Retryable: true,
				}},
				IsError: true,
			}, modern), nil
		}
		diagnostics, err := backend.OperationDiagnostics(ctx, arguments.OperationID, arguments.MaxBytes)
		if err != nil {
			return s.decorateCallResult(actionToolError(err), modern), nil
		}
		return s.decorateCallResult(callToolResult{
			Content: []textContent{{Type: "text", Text: fmt.Sprintf(
				"Switchyard returned %d bounded log excerpts for operation %s.", len(diagnostics.Excerpts), diagnostics.OperationID,
			)}},
			StructuredContent: operationDiagnosticsOutput{Diagnostics: diagnostics},
		}, modern), nil
	case "switchyard_doctor":
		var arguments struct{}
		if err := decodeOptionalObject(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid doctor arguments"}
		}
		report := s.Backend.Doctor(ctx)
		text := "Switchyard connection checks passed."
		if !report.Healthy {
			text = "Switchyard connection checks need attention."
		}
		return s.decorateCallResult(callToolResult{
			Content:           []textContent{{Type: "text", Text: text}},
			StructuredContent: doctorOutput{Doctor: report},
			IsError:           !report.Healthy,
		}, modern), nil
	case "switchyard_start":
		var arguments startArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid start arguments"}
		}
		request := contractv1.StartEnvironmentRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: arguments.RequestID,
				IdempotencyKey:              arguments.IdempotencyKey,
				ExpectedEnvironmentRevision: arguments.ExpectedEnvironmentRevision,
			},
			WorktreeID: arguments.WorktreeID, TargetID: arguments.TargetID,
			ConfirmedTargetID: arguments.ConfirmedTargetID,
			ServiceIDs:        arguments.ServiceIDs,
		}
		if request.Validate() != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid start arguments"}
		}
		receipt, err := s.Backend.StartEnvironment(ctx, request)
		if err != nil {
			return s.decorateCallResult(actionToolError(err), modern), nil
		}
		return s.decorateCallResult(s.mutationResult(ctx, receipt, "start"), modern), nil
	case "switchyard_stop":
		var arguments stopArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil || arguments.EnvironmentID == "" {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid stop arguments"}
		}
		request := contractv1.StopEnvironmentRequest{MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: arguments.RequestID,
			IdempotencyKey:              arguments.IdempotencyKey,
			ExpectedEnvironmentRevision: arguments.ExpectedEnvironmentRevision,
		}}
		if request.Validate() != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid stop arguments"}
		}
		receipt, err := s.Backend.StopEnvironment(ctx, arguments.EnvironmentID, request)
		if err != nil {
			return s.decorateCallResult(actionToolError(err), modern), nil
		}
		return s.decorateCallResult(s.mutationResult(ctx, receipt, "stop"), modern), nil
	case "switchyard_create_worktree":
		var arguments createWorktreeArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid create worktree arguments"}
		}
		backend, available := s.Backend.(WorkspaceBackend)
		if !available {
			return s.decorateCallResult(actionToolError(errors.New("workspace actions unavailable")), modern), nil
		}
		request := contractv1.CreateWorktreeRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: arguments.RequestID,
				IdempotencyKey: arguments.IdempotencyKey,
			},
			RepositoryID: arguments.RepositoryID, Branch: arguments.Branch, StartPoint: arguments.StartPoint,
		}
		if request.Validate() != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid create worktree arguments"}
		}
		receipt, err := backend.CreateWorktree(ctx, request)
		if err != nil {
			return s.decorateCallResult(actionToolError(err), modern), nil
		}
		return s.decorateCallResult(s.mutationResult(ctx, receipt, "create worktree"), modern), nil
	case "switchyard_archive_worktree":
		var arguments archiveWorktreeArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid archive worktree arguments"}
		}
		backend, available := s.Backend.(WorkspaceBackend)
		if !available {
			return s.decorateCallResult(actionToolError(errors.New("workspace actions unavailable")), modern), nil
		}
		request := contractv1.ArchiveWorktreeRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: arguments.RequestID,
				IdempotencyKey: arguments.IdempotencyKey,
			},
			WorktreeID: arguments.WorktreeID,
		}
		if request.Validate() != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid archive worktree arguments"}
		}
		receipt, err := backend.ArchiveWorktree(ctx, request)
		if err != nil {
			return s.decorateCallResult(actionToolError(err), modern), nil
		}
		return s.decorateCallResult(s.mutationResult(ctx, receipt, "archive worktree"), modern), nil
	case "switchyard_adopt_worktree":
		var arguments archiveWorktreeArguments
		if err := decodeParams(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid adopt worktree arguments"}
		}
		backend, available := s.Backend.(WorkspaceBackend)
		if !available {
			return s.decorateCallResult(actionToolError(errors.New("workspace actions unavailable")), modern), nil
		}
		request := contractv1.AdoptWorktreeRequest{
			MutationRequest: contractv1.MutationRequest{
				SchemaVersion: contractv1.SchemaVersion, RequestID: arguments.RequestID,
				IdempotencyKey: arguments.IdempotencyKey,
			},
			WorktreeID: arguments.WorktreeID,
		}
		if request.Validate() != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid adopt worktree arguments"}
		}
		receipt, err := backend.AdoptWorktree(ctx, request)
		if err != nil {
			return s.decorateCallResult(actionToolError(err), modern), nil
		}
		return s.decorateCallResult(s.mutationResult(ctx, receipt, "adopt worktree"), modern), nil
	default:
		return callToolResult{}, &responseError{Code: -32602, Message: "Unknown tool"}
	}
}

func environmentStatusSummary(status statusview.EnvironmentStatus) string {
	summary := fmt.Sprintf(
		"Environment %s is %s/%s at revision %d.", status.Environment.ID,
		status.Environment.ObservedState, status.Environment.Health, status.Environment.Revision,
	)
	summary += repositoryObservationSummary(status.Repository.Observation)
	for _, service := range status.Environment.Services {
		if service.Run == nil {
			continue
		}
		summary += " Current run is " + service.Run.ID
		if service.Run.SourceRevision != "" {
			summary += " from source " + service.Run.SourceRevision
			if service.Run.SourceHasTrackedChanges || service.Run.SourceHasUntrackedFiles {
				summary += " (dirty)"
			}
		}
		summary += "."
		break
	}
	if len(status.Operations) == 0 {
		return summary
	}
	latest := status.Operations[0]
	if latest.State == "pending" || latest.State == "running" {
		if latest.Phase != "" {
			return summary + fmt.Sprintf(" Active %s operation is in phase %s.", latest.Kind, latest.Phase)
		}
		return summary + fmt.Sprintf(" Active %s operation is %s.", latest.Kind, latest.State)
	}
	if latest.State == "failed" && latest.Error != nil {
		failure := " Latest " + latest.Kind + " operation failed"
		if latest.Error.Phase != "" {
			failure += " during " + latest.Error.Phase
		}
		failure += ": " + latest.Error.Message
		if latest.Error.Diagnostic != "" {
			failure += " Diagnostic: " + latest.Error.Diagnostic
		}
		return summary + failure
	}
	return summary
}

func repositoryObservationSummary(observation *contractv1.RepositoryObservation) string {
	if observation == nil {
		return " Repository observation freshness is unavailable."
	}
	if observation.Stale {
		observed := "no successful observation"
		if observation.ObservedAt != nil {
			observed = "last successful observation " + observation.ObservedAt.UTC().Format(time.RFC3339)
		}
		return fmt.Sprintf(
			" Repository data is stale (%s; attempt %s failed with %s).",
			observed, observation.LastAttemptAt.UTC().Format(time.RFC3339), observation.ErrorCode,
		)
	}
	if observation.ObservedAt == nil {
		return " Repository observation freshness is unavailable."
	}
	return " Repository observed at " + observation.ObservedAt.UTC().Format(time.RFC3339) + "."
}

func toolDefinitions() []toolDefinition {
	readAnnotations := toolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
	}
	return []toolDefinition{
		{
			Name:        "switchyard_context",
			Description: "Read only the Switchyard state for the agent's active worktree. Call this first for requests about 'this worktree', 'here', or the current task, and pass the physical absolute workspace path. If a host path hint is rejected, obtain pwd -P read-only and retry. Never guess from a branch name or use global inventory as an implicit current-worktree selection.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"worktreePath": map[string]any{
						"type":        "string",
						"description": "Physical absolute path of the active workspace or a directory inside it, taken from host context or read-only pwd -P; never infer or abbreviate it.",
					},
				},
				"required": []string{"worktreePath"},
			},
			Annotations: readAnnotations,
		},
		{
			Name:        "switchyard_environment_status",
			Description: "Read one exact Switchyard environment with its worktree, services, URLs, operations, and alerts. Use the environment ID returned by switchyard_context or an accepted mutation receipt when polling for completion. A start/rebuild is complete only when the exact accepted operation succeeded and the current service run ID matches the receipt runId; an older healthy run is not completion.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"environmentId": map[string]any{"type": "string", "description": "Exact opaque environment ID returned by Switchyard."},
				},
				"required": []string{"environmentId"},
			},
			Annotations: readAnnotations,
		},
		{
			Name:        "switchyard_operation_diagnostics",
			Description: "Explicitly read bounded excerpts from the Switchyard-owned stdout and stderr logs referenced by a failed operation. Call only when the structured diagnostic from switchyard_environment_status is insufficient; raw excerpts stay out of normal status responses to conserve the agent context. Switchyard applies safety redactions and returns only owned log files.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"operationId": map[string]any{"type": "string", "description": "Exact operation ID carrying the logReference."},
					"maxBytes": map[string]any{
						"type": "integer", "minimum": 256, "maximum": 32768,
						"description": "Optional maximum bytes read from the tail of each log stream; omitted defaults to 8192.",
					},
				},
				"required": []string{"operationId"},
			},
			Annotations: readAnnotations,
		},
		{
			Name:        "switchyard_inventory",
			Description: "Read the global Switchyard inventory across every configured repository, worktree, and environment. Use only for explicit cross-worktree discovery, comparison, or resolving a repository for worktree creation; do not treat the first or similarly named entry as the current worktree.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
			Annotations: readAnnotations,
		},
		{
			Name:        "switchyard_doctor",
			Description: "Check local Switchyard runtime files, daemon identity, and status connectivity.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
			Annotations: readAnnotations,
		},
		mutationToolDefinition("switchyard_start", "Start selected services for the exact worktree resolved by switchyard_context and return immediately with an operation receipt. If the selected context target has warnOnStart=true, ask the human user for explicit approval before this call and acknowledge that exact target.", false, map[string]any{
			"requestId":      map[string]any{"type": "string", "description": "Caller-generated opaque request ID."},
			"idempotencyKey": map[string]any{"type": "string", "description": "Stable key reused when retrying this exact start."},
			"worktreeId":     map[string]any{"type": "string", "description": "Exact worktree ID from switchyard_context."},
			"targetId":       map[string]any{"type": "string", "description": "Repository-configured target ID from switchyard_context; omitted uses the repository default."},
			"confirmedTargetId": map[string]any{
				"type":        "string",
				"description": "Exact target ID acknowledged after explicit human approval for this start. Required when that target has warnOnStart=true. Never infer approval or supply this field before asking the user.",
			},
			"serviceIds": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 32, "uniqueItems": true,
				"items": map[string]any{"type": "string"},
			},
			"expectedEnvironmentRevision": map[string]any{"type": "integer", "minimum": 0},
		}, []string{"requestId", "idempotencyKey", "worktreeId", "serviceIds"}),
		mutationToolDefinition("switchyard_stop", "Stop one owned Switchyard environment and return immediately with an operation receipt.", true, map[string]any{
			"requestId":                   map[string]any{"type": "string", "description": "Caller-generated opaque request ID."},
			"idempotencyKey":              map[string]any{"type": "string", "description": "Stable key reused when retrying this exact stop."},
			"environmentId":               map[string]any{"type": "string", "description": "Exact environment ID from switchyard_context or switchyard_environment_status."},
			"expectedEnvironmentRevision": map[string]any{"type": "integer", "minimum": 0},
		}, []string{"requestId", "idempotencyKey", "environmentId"}),
		mutationToolDefinition("switchyard_create_worktree", "Create a Switchyard-managed Git worktree and branch. The helper restarts after success so the new worktree becomes available for workspace preparation and environment start.", false, map[string]any{
			"requestId":      map[string]any{"type": "string", "description": "Caller-generated opaque request ID."},
			"idempotencyKey": map[string]any{"type": "string", "description": "Stable key reused when retrying this exact creation."},
			"repositoryId":   map[string]any{"type": "string", "description": "Exact repository ID from switchyard_inventory."},
			"branch":         map[string]any{"type": "string", "description": "New Git branch name."},
			"startPoint":     map[string]any{"type": "string", "description": "Optional Git base; omitted uses the repository configuration default."},
		}, []string{"requestId", "idempotencyKey", "repositoryId", "branch"}),
		mutationToolDefinition("switchyard_adopt_worktree", "Transfer an existing clean, pushed, non-primary worktree inside the configured managed root into Switchyard ownership. The helper restarts after success.", false, map[string]any{
			"requestId":      map[string]any{"type": "string", "description": "Caller-generated opaque request ID."},
			"idempotencyKey": map[string]any{"type": "string", "description": "Stable key reused when retrying this exact adoption."},
			"worktreeId":     map[string]any{"type": "string", "description": "Exact adopted worktree ID from switchyard_context."},
		}, []string{"requestId", "idempotencyKey", "worktreeId"}),
		mutationToolDefinition("switchyard_archive_worktree", "Archive one Switchyard-managed worktree. Refuses primary, active, dirty, unpushed, foreign, or unverifiable worktrees.", true, map[string]any{
			"requestId":      map[string]any{"type": "string", "description": "Caller-generated opaque request ID."},
			"idempotencyKey": map[string]any{"type": "string", "description": "Stable key reused when retrying this exact archive."},
			"worktreeId":     map[string]any{"type": "string", "description": "Exact managed worktree ID from switchyard_context."},
		}, []string{"requestId", "idempotencyKey", "worktreeId"}),
	}
}

func mutationToolDefinition(
	name string,
	description string,
	destructive bool,
	properties map[string]any,
	required []string,
) toolDefinition {
	return toolDefinition{
		Name: name, Description: description,
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": properties, "required": required,
		},
		Annotations: toolAnnotations{
			ReadOnlyHint: false, DestructiveHint: destructive, IdempotentHint: true, OpenWorldHint: false,
		},
	}
}

func worktreeCount(repositories []contractv1.Repository) int {
	count := 0
	for _, repository := range repositories {
		count += len(repository.Worktrees)
	}
	return count
}

func (s Server) mutationResult(
	ctx context.Context,
	receipt contractv1.MutationReceipt,
	action string,
) callToolResult {
	output := mutationOutput{Receipt: receipt}
	resource := "workspace"
	if receipt.EnvironmentID != "" {
		resource = "environment"
		if snapshot, err := s.Backend.Status(ctx); err == nil {
			if footer, footerErr := BuildEnvironmentContext(snapshot, receipt.EnvironmentID); footerErr == nil {
				output.EnvironmentContext = footer
			}
		}
	}
	message := fmt.Sprintf("Switchyard accepted %s %s operation %s.", resource, action, receipt.OperationID)
	if receipt.RunID != "" {
		message = fmt.Sprintf(
			"Switchyard accepted %s %s operation %s for run %s.",
			resource, action, receipt.OperationID, receipt.RunID,
		)
	}
	return callToolResult{
		Content: []textContent{{Type: "text", Text: fmt.Sprintf(
			"%s", message,
		)}},
		StructuredContent: output,
	}
}

func daemonToolError(err error) callToolResult {
	code := apiclient.CodeOf(err)
	return callToolResult{
		Content: []textContent{{Type: "text", Text: "Switchyard could not read daemon status."}},
		StructuredContent: toolErrorOutput{Error: toolErrorDetails{
			Code:    string(code),
			Message: "Switchyard could not read daemon status.",
		}},
		IsError: true,
	}
}

func actionToolError(err error) callToolResult {
	code := apiclient.CodeOf(err)
	message := "Switchyard could not accept the requested action."
	details := toolErrorDetails{Code: string(code), Message: message}
	if contractError, ok := apiclient.ContractErrorOf(err); ok {
		message = contractError.Message
		details = toolErrorDetails{
			Code: contractError.Code, Message: contractError.Message, Retryable: contractError.Retryable,
			ResourceKind: contractError.ResourceKind, ResourceID: contractError.ResourceID,
			CurrentState: contractError.CurrentState, RequestedState: contractError.RequestedState,
			Phase: contractError.Phase, Step: contractError.Step, Diagnostic: contractError.Diagnostic,
			LogReference: contractError.LogReference, NextAction: contractError.NextAction,
			ExitCode: contractError.ExitCode,
		}
	}
	return callToolResult{
		Content:           []textContent{{Type: "text", Text: message}},
		StructuredContent: toolErrorOutput{Error: details},
		IsError:           true,
	}
}

func (s Server) modernResultFields() modernResultFields {
	return modernResultFields{
		ResultType: "complete",
		Meta: map[string]any{
			metaServerInfo: implementationMetadata{Name: s.Name, Version: s.Version},
		},
	}
}

func (s Server) decorateCallResult(result callToolResult, modern bool) callToolResult {
	if modern {
		result.modernResultFields = s.modernResultFields()
	}
	return result
}

func validateModernMetadata(contents json.RawMessage) (bool, *responseError) {
	if len(contents) == 0 || bytes.Equal(bytes.TrimSpace(contents), []byte("null")) {
		return false, nil
	}
	var parameters map[string]json.RawMessage
	if err := json.Unmarshal(contents, &parameters); err != nil || parameters == nil {
		return false, &responseError{Code: -32602, Message: "Invalid request parameters"}
	}
	rawMetadata, hasMetadata := parameters["_meta"]
	if !hasMetadata {
		return false, nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(rawMetadata, &metadata); err != nil || metadata == nil {
		return false, &responseError{Code: -32602, Message: "Invalid request metadata"}
	}
	rawVersion, hasVersion := metadata[metaProtocolVersion]
	rawCapabilities, hasCapabilities := metadata[metaClientCapabilities]
	_, hasClientInfo := metadata[metaClientInfo]
	if !hasVersion && !hasCapabilities && !hasClientInfo {
		return false, nil
	}
	if !hasVersion || !hasCapabilities {
		return false, &responseError{Code: -32602, Message: "Required request metadata is missing"}
	}
	var version string
	if err := json.Unmarshal(rawVersion, &version); err != nil || version == "" {
		return false, &responseError{Code: -32602, Message: "Invalid protocol version metadata"}
	}
	if version != ProtocolVersion {
		return false, &responseError{
			Code:    -32022,
			Message: "Unsupported protocol version",
			Data: map[string]any{
				"supported": []string{ProtocolVersion},
				"requested": version,
			},
		}
	}
	if !validJSONObject(rawCapabilities) {
		return false, &responseError{Code: -32602, Message: "Invalid client capabilities metadata"}
	}
	if rawClientInfo, exists := metadata[metaClientInfo]; exists && !validImplementation(rawClientInfo) {
		return false, &responseError{Code: -32602, Message: "Invalid client information metadata"}
	}
	return true, nil
}

func validJSONObject(contents json.RawMessage) bool {
	if len(contents) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(contents, &object) == nil && object != nil
}

func validImplementation(contents json.RawMessage) bool {
	if !validJSONObject(contents) {
		return false
	}
	var implementation implementationMetadata
	return json.Unmarshal(contents, &implementation) == nil &&
		implementation.Name != "" && implementation.Version != ""
}

func decodeParams(contents json.RawMessage, destination any) error {
	if len(contents) == 0 || bytes.Equal(bytes.TrimSpace(contents), []byte("null")) {
		return errors.New("parameters are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireDecodeEnd(decoder)
}

func decodePermissiveParams(contents json.RawMessage, destination any) error {
	if len(contents) == 0 || bytes.Equal(bytes.TrimSpace(contents), []byte("null")) {
		return errors.New("parameters are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireDecodeEnd(decoder)
}

func negotiateLegacyProtocolVersion(requested string) string {
	switch requested {
	case LegacyProtocolVersion, LegacyPreviousProtocolVersion:
		return requested
	default:
		return LegacyProtocolVersion
	}
}

func decodeOptionalObject(contents json.RawMessage, destination any) error {
	if len(contents) == 0 || bytes.Equal(bytes.TrimSpace(contents), []byte("null")) {
		contents = []byte("{}")
	}
	return decodeParams(contents, destination)
}

func requireDecodeEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON values are not accepted")
}

func success(id *json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func protocolError(id *json.RawMessage, code int, message string) response {
	return protocolErrorWithData(id, code, message, nil)
}

func protocolErrorWithData(id *json.RawMessage, code int, message string, data any) response {
	if id == nil {
		nullID := json.RawMessage("null")
		id = &nullID
	}
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &responseError{Code: code, Message: message, Data: data},
	}
}

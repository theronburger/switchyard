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
)

const (
	ProtocolVersion = "2025-11-25"
	maximumMessage  = 1024 * 1024
)

type Backend interface {
	Status(context.Context) (contractv1.StatusSnapshot, error)
	Doctor(context.Context) apiclient.DoctorReport
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
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
	Task      json.RawMessage `json:"task,omitempty"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callToolResult struct {
	Content           []textContent `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

type statusArguments struct {
	EnvironmentID string `json:"environmentId,omitempty"`
}

type statusOutput struct {
	Status             contractv1.StatusSnapshot      `json:"status"`
	EnvironmentContext *contractv1.EnvironmentContext `json:"environmentContext,omitempty"`
}

type doctorOutput struct {
	Doctor apiclient.DoctorReport `json:"doctor"`
}

type toolErrorOutput struct {
	Error toolErrorDetails `json:"error"`
}

type toolErrorDetails struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type sessionState struct {
	initializeAccepted bool
	ready              bool
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
		if incoming.ID == nil || state.initializeAccepted {
			return protocolError(incoming.ID, -32600, "Invalid initialize request"), incoming.ID != nil
		}
		var params initializeParams
		if err := decodePermissiveParams(incoming.Params, &params); err != nil || params.ProtocolVersion == "" {
			return protocolError(incoming.ID, -32602, "Invalid initialize parameters"), true
		}
		state.initializeAccepted = true
		return success(incoming.ID, initializeResult{
			ProtocolVersion: negotiateProtocolVersion(params.ProtocolVersion),
			Capabilities:    map[string]any{"tools": map[string]any{}},
			ServerInfo:      implementationMetadata{Name: s.Name, Version: s.Version},
		}), true
	case "notifications/initialized":
		if state.initializeAccepted {
			state.ready = true
		}
		return response{}, false
	case "ping":
		if incoming.ID == nil {
			return response{}, false
		}
		return success(incoming.ID, map[string]any{}), true
	}

	if !state.ready {
		if incoming.ID == nil {
			return response{}, false
		}
		return protocolError(incoming.ID, -32600, "Server is not initialized"), true
	}
	if incoming.ID == nil {
		return response{}, false
	}

	switch incoming.Method {
	case "tools/list":
		return success(incoming.ID, map[string]any{"tools": toolDefinitions()}), true
	case "tools/call":
		result, protocolFailure := s.callTool(ctx, incoming.Params)
		if protocolFailure != nil {
			return protocolError(incoming.ID, protocolFailure.Code, protocolFailure.Message), true
		}
		return success(incoming.ID, result), true
	default:
		return protocolError(incoming.ID, -32601, "Method not found"), true
	}
}

func (s Server) callTool(ctx context.Context, rawParams json.RawMessage) (callToolResult, *responseError) {
	var params callToolParams
	if err := decodeParams(rawParams, &params); err != nil || params.Name == "" || len(params.Task) != 0 {
		return callToolResult{}, &responseError{Code: -32602, Message: "Invalid tool call parameters"}
	}

	switch params.Name {
	case "switchyard_status":
		var arguments statusArguments
		if err := decodeOptionalObject(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid status arguments"}
		}
		snapshot, err := s.Backend.Status(ctx)
		if err != nil {
			return daemonToolError(err), nil
		}
		footer, err := BuildEnvironmentContext(snapshot, arguments.EnvironmentID)
		if err != nil {
			return callToolResult{
				Content: []textContent{{Type: "text", Text: "The requested environment was not found."}},
				StructuredContent: toolErrorOutput{Error: toolErrorDetails{
					Code:    "ENVIRONMENT_NOT_FOUND",
					Message: "The requested environment was not found.",
				}},
				IsError: true,
			}, nil
		}
		return callToolResult{
			Content: []textContent{{
				Type: "text",
				Text: fmt.Sprintf(
					"Switchyard status revision %d with %d environment(s).",
					snapshot.SnapshotRevision,
					len(snapshot.Environments)),
			}},
			StructuredContent: statusOutput{Status: snapshot, EnvironmentContext: footer},
		}, nil
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
		return callToolResult{
			Content:           []textContent{{Type: "text", Text: text}},
			StructuredContent: doctorOutput{Doctor: report},
			IsError:           !report.Healthy,
		}, nil
	default:
		return callToolResult{}, &responseError{Code: -32602, Message: "Unknown tool"}
	}
}

func toolDefinitions() []toolDefinition {
	annotations := toolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
	}
	return []toolDefinition{
		{
			Name:        "switchyard_status",
			Description: "Read Switchyard daemon status, optionally scoped to an environment.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"environmentId": map[string]any{
						"type":        "string",
						"description": "Opaque Switchyard environment ID for a scoped state footer.",
					},
				},
			},
			Annotations: annotations,
		},
		{
			Name:        "switchyard_doctor",
			Description: "Check local Switchyard runtime files, daemon identity, and status connectivity.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{},
			},
			Annotations: annotations,
		},
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

func negotiateProtocolVersion(requested string) string {
	switch requested {
	case ProtocolVersion, "2025-06-18":
		return requested
	default:
		return ProtocolVersion
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
	if id == nil {
		nullID := json.RawMessage("null")
		id = &nullID
	}
	return response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &responseError{Code: code, Message: message},
	}
}

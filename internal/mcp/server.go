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
			Instructions:       "Read Switchyard daemon status and connection diagnostics.",
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
	case "switchyard_status":
		var arguments statusArguments
		if err := decodeOptionalObject(params.Arguments, &arguments); err != nil {
			return callToolResult{}, &responseError{Code: -32602, Message: "Invalid status arguments"}
		}
		snapshot, err := s.Backend.Status(ctx)
		if err != nil {
			return s.decorateCallResult(daemonToolError(err), modern), nil
		}
		footer, err := BuildEnvironmentContext(snapshot, arguments.EnvironmentID)
		if err != nil {
			return s.decorateCallResult(callToolResult{
				Content: []textContent{{Type: "text", Text: "The requested environment was not found."}},
				StructuredContent: toolErrorOutput{Error: toolErrorDetails{
					Code:    "ENVIRONMENT_NOT_FOUND",
					Message: "The requested environment was not found.",
				}},
				IsError: true,
			}, modern), nil
		}
		return s.decorateCallResult(callToolResult{
			Content: []textContent{{
				Type: "text",
				Text: fmt.Sprintf(
					"Switchyard status revision %d with %d environment(s).",
					snapshot.SnapshotRevision,
					len(snapshot.Environments)),
			}},
			StructuredContent: statusOutput{Status: snapshot, EnvironmentContext: footer},
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

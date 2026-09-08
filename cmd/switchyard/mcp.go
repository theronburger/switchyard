package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path/filepath"

	"github.com/theronburger/switchyard/internal/localapi"
	"github.com/theronburger/switchyard/internal/workspaces"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}
type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema map[string]any  `json:"inputSchema"`
	Annotations map[string]bool `json:"annotations"`
}

func mcpTools() []tool {
	stringProperty := map[string]any{"type": "string"}
	services := map[string]any{"type": "array", "items": stringProperty}
	makeTool := func(name, description string, readOnly bool, required []string, properties map[string]any) tool {
		return tool{Name: name, Description: description, InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, Annotations: map[string]bool{"readOnlyHint": readOnly, "destructiveHint": name == "switchyard_prune_worktree", "openWorldHint": false}}
	}
	path := map[string]any{"worktreePath": stringProperty}
	return []tool{
		makeTool("switchyard_context", "Read this exact workspace. Supply the physical absolute working directory. Includes current run, services, URLs, choices and Git state.", true, []string{"worktreePath"}, path),
		makeTool("switchyard_inventory", "List configured repositories and every Git worktree. Use for deliberate cross-workspace discovery.", true, []string{}, map[string]any{}),
		makeTool("switchyard_start", "Run selected services in this workspace. Setup runs automatically. Returns accepted run; poll context until that exact run ID is running. If target.confirm is true, ask the user first and supply confirmedTargetId for that target.", false, []string{"worktreePath"}, map[string]any{"worktreePath": stringProperty, "serviceIds": services, "targetId": stringProperty, "confirmedTargetId": stringProperty, "requestId": stringProperty}),
		makeTool("switchyard_prepare", "Run this workspace's ordinary setup commands without starting services. Poll the exact run ID until stopped with step Prepared or failed.", false, []string{"worktreePath"}, path),
		makeTool("switchyard_stop", "Stop this workspace's services or cancel its current setup/start. Other workspaces keep running.", false, []string{"worktreePath"}, path),
		makeTool("switchyard_configure", "Remember this workspace's target and selected services. Does not run them or edit repository commands.", false, []string{"worktreePath", "targetId", "serviceIds"}, map[string]any{"worktreePath": stringProperty, "targetId": stringProperty, "serviceIds": services}),
		makeTool("switchyard_logs", "Read bounded redacted output for this workspace's current run. Use when its status error is insufficient.", true, []string{"worktreePath"}, path),
		makeTool("switchyard_prune_plan", "Preview removal of this exact worktree and concrete blockers. No changes are made.", true, []string{"worktreePath"}, path),
		makeTool("switchyard_prune_worktree", "Remove this exact Git worktree only when the user requested deletion and has reviewed the prune plan. Rechecks dirty/unpushed/locked/primary/running blockers. Never uses force.", false, []string{"worktreePath"}, path),
		makeTool("switchyard_create_worktree", "Create a branch and ordinary Git worktree for the named repository. Returns its path. It is immediately usable with no adoption step.", false, []string{"repositoryId", "branch"}, map[string]any{"repositoryId": stringProperty, "branch": stringProperty, "base": stringProperty}),
	}
}
func serveMCP(client localapi.Client, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request rpcRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			if err := encoder.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: map[string]any{"code": -32700, "message": "Invalid JSON"}}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(request.Params, &params)
			protocol := "2025-11-25"
			switch params.ProtocolVersion {
			case "2026-07-28", "2025-11-25", "2025-06-18", "2024-11-05":
				protocol = params.ProtocolVersion
			}
			response.Result = map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "switchyard", "version": version}, "instructions": "Use exact workspace paths. Git worktrees require no ownership or adoption. Start includes setup; read the returned run ID until running. Never infer one workspace from inventory ordering."}
		case "ping":
			response.Result = map[string]any{}
		case "tools/list":
			response.Result = map[string]any{"tools": mcpTools()}
		case "tools/call":
			var call struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			err := json.Unmarshal(request.Params, &call)
			var result any
			if err == nil {
				result, err = callTool(context.Background(), client, call.Name, call.Arguments)
			}
			if err != nil {
				response.Result = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": err.Error()}}}
			} else {
				data, _ := json.Marshal(result)
				response.Result = map[string]any{"structuredContent": result, "content": []any{map[string]string{"type": "text", "text": string(data)}}}
			}
		default:
			response.Error = map[string]any{"code": -32601, "message": "Unknown method"}
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func callTool(ctx context.Context, client localapi.Client, name string, raw json.RawMessage) (any, error) {
	var definition *tool
	for _, candidate := range mcpTools() {
		if candidate.Name == name {
			definition = &candidate
			break
		}
	}
	if definition == nil {
		return nil, errors.New("Unknown Switchyard tool.")
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var supplied map[string]json.RawMessage
	if json.Unmarshal(raw, &supplied) != nil || supplied == nil {
		return nil, errors.New("Tool arguments must be an object.")
	}
	for field := range supplied {
		if _, known := definition.InputSchema["properties"].(map[string]any)[field]; !known {
			return nil, errors.New("Unknown tool argument.")
		}
	}
	for _, field := range definition.InputSchema["required"].([]string) {
		if _, present := supplied[field]; !present {
			return nil, errors.New("A required tool argument is missing.")
		}
	}

	var args struct {
		WorktreePath      string   `json:"worktreePath"`
		ServiceIDs        []string `json:"serviceIds"`
		TargetID          string   `json:"targetId"`
		ConfirmedTargetID string   `json:"confirmedTargetId"`
		RequestID         string   `json:"requestId"`
		RepositoryID      string   `json:"repositoryId"`
		Branch            string   `json:"branch"`
		Base              string   `json:"base"`
	}
	if len(raw) != 0 && json.Unmarshal(raw, &args) != nil {
		return nil, errors.New("Invalid tool arguments.")
	}
	if name == "switchyard_inventory" {
		var result workspaces.Snapshot
		err := client.Request(ctx, "GET", "/api/status", nil, &result)
		return result, err
	}
	if name == "switchyard_create_worktree" {
		var result map[string]string
		err := client.Request(ctx, "POST", "/api/create", workspaces.CreateRequest{RepositoryID: args.RepositoryID, Branch: args.Branch, Base: args.Base}, &result)
		return result, err
	}
	if !filepath.IsAbs(args.WorktreePath) {
		return nil, errors.New("Supply the physical absolute workspace path.")
	}
	workspace, err := resolveWorkspace(ctx, client, args.WorktreePath)
	if err != nil {
		return nil, err
	}
	path := workspace.Path
	switch name {
	case "switchyard_context":
		var snapshot workspaces.Snapshot
		if err := client.Request(ctx, "GET", "/api/status", nil, &snapshot); err != nil {
			return nil, err
		}
		for _, repository := range snapshot.Repositories {
			if repository.ID == workspace.RepositoryID {
				return map[string]any{"workspace": workspace, "repository": repository}, nil
			}
		}
		return nil, errors.New("The repository configuration changed. Read context again.")
	case "switchyard_start", "switchyard_prepare":
		id := args.RequestID
		if id == "" {
			id = randomID()
		}
		target := args.TargetID
		if target == "" {
			target = workspace.Choices.Target
		}
		selected := args.ServiceIDs
		if selected == nil {
			selected = workspace.Choices.Services
		}
		endpoint := "/api/run"
		if name == "switchyard_prepare" {
			endpoint = "/api/prepare"
		}
		var result workspaces.Run
		err := client.Request(ctx, "POST", endpoint, workspaces.RunRequest{Path: path, RequestID: id, Target: target, Services: selected, ConfirmedTarget: args.ConfirmedTargetID}, &result)
		return result, err
	case "switchyard_stop":
		var result workspaces.Run
		err := client.Request(ctx, "POST", "/api/stop", map[string]string{"path": path}, &result)
		return result, err
	case "switchyard_configure":
		err := client.Request(ctx, "POST", "/api/choices", map[string]any{"path": path, "target": args.TargetID, "services": args.ServiceIDs}, nil)
		return map[string]bool{"saved": err == nil}, err
	case "switchyard_logs":
		var result workspaces.LogOutput
		err := client.Request(ctx, "GET", "/api/logs?path="+url.QueryEscape(path), nil, &result)
		return result, err
	case "switchyard_prune_plan":
		var result workspaces.PrunePlan
		err := client.Request(ctx, "GET", "/api/prune-plan?path="+url.QueryEscape(path), nil, &result)
		return result, err
	case "switchyard_prune_worktree":
		err := client.Request(ctx, "POST", "/api/prune", map[string]string{"path": path}, nil)
		return map[string]bool{"removed": err == nil}, err
	default:
		return nil, errors.New("Unknown Switchyard tool.")
	}
}

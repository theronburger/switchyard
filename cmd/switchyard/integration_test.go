package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/localapi"
	"github.com/theronburger/switchyard/internal/workspaces"
)

func TestDaemonCLIAndMCPTwoWorkspaces(t *testing.T) {
	if testing.Short() {
		t.Skip("real daemon acceptance")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "sy")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	git := func(directory string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", directory}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := filepath.Join(root, "repository")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".prepared\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-m", "fixture")
	git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	second := filepath.Join(root, "second workspace")
	git(repo, "worktree", "add", "-b", "second", second)
	repo, _ = filepath.EvalSymlinks(repo)
	second, _ = filepath.EvalSymlinks(second)
	config := workspaces.Config{SchemaVersion: 1, Repositories: []workspaces.RepositoryConfig{{ID: "fixture", Name: "Fixture", Path: repo, DefaultTarget: "local", Targets: []workspaces.Target{{ID: "local", Name: "Local"}}, Setup: []workspaces.Command{{Script: "test ! -f fail-setup && touch .prepared"}}, Services: []workspaces.ServiceConfig{{ID: "web", Name: "Web", Kind: "web", Command: workspaces.Command{Script: `exec /usr/bin/python3 -u -c 'import http.server,os; H=type("H",(http.server.BaseHTTPRequestHandler,),{"do_GET":lambda s:(s.send_response(200),s.end_headers(),s.wfile.write(os.getcwd().encode()))}); http.server.HTTPServer(("127.0.0.1",int(os.environ["PORT"])),H).serve_forever()'`}, Ports: []workspaces.Port{{Name: "http", Environment: "PORT", URL: true}}, Readiness: workspaces.Readiness{Port: "http", Path: "/", TimeoutSeconds: 10}}}}}}
	support := filepath.Join(root, "support")
	if err := workspaces.SaveConfig(filepath.Join(support, "config.json"), config); err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command(binary, "daemon", "--root", support)
	var daemonOutput bytes.Buffer
	daemon.Stdout = &daemonOutput
	daemon.Stderr = &daemonOutput
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	alive := true
	defer func() {
		if alive {
			_ = daemon.Process.Signal(syscall.SIGTERM)
			_ = daemon.Wait()
		}
	}()
	client := localapi.Client{Root: support}
	ctx := context.Background()
	eventually(t, 10*time.Second, func() bool { return client.Request(ctx, "GET", "/api/status", nil, new(workspaces.Snapshot)) == nil })
	cli := func(args ...string) []byte {
		t.Helper()
		c := exec.Command(binary, append(args, "--root", support, "--json")...)
		out, err := c.CombinedOutput()
		if err != nil {
			var logs workspaces.LogOutput
			_ = client.Request(ctx, "GET", "/api/logs?path="+url.QueryEscape(args[1]), nil, &logs)
			t.Fatalf("CLI %v: %v %s; logs %s", args, err, out, logs.Text)
		}
		return out
	}
	var firstRun workspaces.Run
	if err := json.Unmarshal(cli("run", repo, "web", "--wait"), &firstRun); err != nil {
		t.Fatal(err)
	}
	mcp := exec.Command(binary, "mcp", "--root", support)
	input, err := mcp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := mcp.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = mcp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = mcp.Wait() }()
	encoder := json.NewEncoder(input)
	reader := bufio.NewReader(output)
	call := func(id int, method string, params any) map[string]json.RawMessage {
		t.Helper()
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var reply map[string]json.RawMessage
		if err = json.Unmarshal(line, &reply); err != nil {
			t.Fatal(err)
		}
		if reply["error"] != nil {
			t.Fatalf("RPC failure %s", line)
		}
		return reply
	}
	call(1, "initialize", map[string]string{"protocolVersion": "2025-11-25"})
	response := call(2, "tools/call", map[string]any{"name": "switchyard_start", "arguments": map[string]any{"worktreePath": second, "serviceIds": []string{"web"}, "requestId": "second-once"}})
	var toolResult struct {
		IsError    bool           `json:"isError"`
		Structured workspaces.Run `json:"structuredContent"`
	}
	if err = json.Unmarshal(response["result"], &toolResult); err != nil {
		t.Fatal(err)
	}
	if toolResult.IsError || toolResult.Structured.ID == "" {
		t.Fatalf("MCP failed %s", response["result"])
	}
	secondRun, err := waitForRun(ctx, client, second, toolResult.Structured.ID, false)
	if err != nil {
		var logs workspaces.LogOutput
		_ = client.Request(ctx, "GET", "/api/logs?path="+url.QueryEscape(second), nil, &logs)
		t.Fatalf("%v; logs: %s", err, logs.Text)
	}
	if firstRun.ID == secondRun.ID || firstRun.URLs["web.http"] == secondRun.URLs["web.http"] {
		t.Fatal("instances were not isolated")
	}
	checkHTTP := func(address, want string) {
		t.Helper()
		r, err := http.Get(address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		data, _ := io.ReadAll(r.Body)
		if string(data) != want {
			t.Fatalf("wrong instance: %q expected %q", data, want)
		}
	}
	checkHTTP(firstRun.URLs["web.http"], repo)
	checkHTTP(secondRun.URLs["web.http"], second)
	var activePlan workspaces.PrunePlan
	if err := client.Request(ctx, "GET", "/api/prune-plan?path="+url.QueryEscape(second), nil, &activePlan); err != nil {
		t.Fatal(err)
	}
	if len(activePlan.Blockers) == 0 {
		t.Fatal("active worktree is removable")
	}
	cli("stop", repo, "--wait")
	checkHTTP(secondRun.URLs["web.http"], second)
	// The daemon's abrupt death must close both the guardian and service, without adoption state.
	if err = daemon.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = daemon.Wait()
	alive = false
	eventually(t, 5*time.Second, func() bool {
		c := http.Client{Timeout: 200 * time.Millisecond}
		r, err := c.Get(secondRun.URLs["web.http"])
		if err == nil {
			_ = r.Body.Close()
		}
		return err != nil
	})
	daemon = exec.Command(binary, "daemon", "--root", support)
	if err = daemon.Start(); err != nil {
		t.Fatal(err)
	}
	alive = true
	eventually(t, 10*time.Second, func() bool {
		var snapshot workspaces.Snapshot
		if client.Request(ctx, "GET", "/api/status", nil, &snapshot) != nil {
			return false
		}
		for _, w := range snapshot.Workspaces {
			if w.Run != nil {
				return false
			}
		}
		return len(snapshot.Workspaces) == 2
	})
	var restored workspaces.Workspace
	if err = json.Unmarshal(cli("status", second), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Choices.Target != "local" || len(restored.Choices.Services) != 1 {
		t.Fatal("choices lost on restart")
	}
	cli("run", second, "web", "--wait")
	cli("stop", second, "--wait")
	cli("prune", second)
	if _, err = os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("prune did not remove exact clean worktree")
	}
	if _, err = os.Stat(repo); err != nil {
		t.Fatal("prune touched primary")
	}
}

func eventually(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition was not reached before timeout")
}

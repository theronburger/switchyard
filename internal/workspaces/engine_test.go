package workspaces

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(tests *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--supervise" {
		os.Exit(Supervise())
	}
	if len(os.Args) > 1 && os.Args[1] == "--test-daemon" {
		runTestDaemon()
		os.Exit(0)
	}
	os.Exit(tests.Run())
}

func TestRuntimeService(t *testing.T) {
	mode := os.Getenv("SWITCHYARD_TEST_SERVICE")
	if mode == "" {
		return
	}
	if _, err := os.Stat("FAIL_START"); err == nil {
		fmt.Fprintln(os.Stderr, "missing startup prerequisite")
		os.Exit(2)
	}
	if _, err := os.Stat("output/ready"); err != nil {
		fmt.Fprintln(os.Stderr, "workspace setup is missing")
		os.Exit(3)
	}
	if mode == "fork" {
		command := exec.Command(os.Args[0], "-test.run=^TestRuntimeService$")
		command.Env = append(os.Environ(), "SWITCHYARD_TEST_SERVICE=child", "PORT="+os.Getenv("CHILD_PORT"))
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if command.Start() != nil {
			os.Exit(4)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+os.Getenv("PORT"))
	if err != nil {
		os.Exit(5)
	}
	root, _ := os.Getwd()
	if mode == "exit-soon" {
		go func() { time.Sleep(300 * time.Millisecond); os.Exit(7) }()
	}
	_, _ = fmt.Fprintln(os.Stdout, "service ready; API_TOKEN=must-not-leak person@example.invalid")
	server := http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if _, err := os.Stat("UNHEALTHY"); err == nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(writer, filepath.Base(root))
	})}
	_ = server.Serve(listener)
	os.Exit(0)
}

func TestEngineTwoWorkspacesSetupRetryPortsChoicesAndStop(t *testing.T) {
	engine, config, primary, linked := engineFixture(t)
	foreign, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	config.Repositories[0].Services[0].Ports[0].Preferred = foreign.Addr().(*net.TCPAddr).Port
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "FAIL_SETUP"), []byte("failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	startEngineRun(t, engine, primary, "first")
	startEngineRun(t, engine, linked, "second")
	first := waitEngineState(t, engine, primary, "running")
	second := waitEngineState(t, engine, linked, "failed")
	if first.ID == second.ID || second.Error == "" {
		t.Fatalf("workspace runs leaked: %+v %+v", first, second)
	}
	assertEngineHTTP(t, first.URLs["web.http"], "primary")
	if err := engine.SetConfig(Config{SchemaVersion: 1, Repositories: []RepositoryConfig{}}); err == nil {
		t.Fatal("configuration hid a running workspace")
	}
	if strings.HasSuffix(first.URLs["web.http"], ":"+fmt.Sprint(foreign.Addr().(*net.TCPAddr).Port)) {
		t.Fatal("foreign port was used")
	}
	if err := os.Remove(filepath.Join(linked, "FAIL_SETUP")); err != nil {
		t.Fatal(err)
	}
	startEngineRun(t, engine, linked, "retry")
	second = waitEngineState(t, engine, linked, "running")
	if !strings.HasPrefix(first.URLs["web.http"], "http://localhost:") {
		t.Fatal("published app URL must use the local development hostname")
	}
	if first.URLs["web.http"] == second.URLs["web.http"] {
		t.Fatal("workspaces share a port")
	}
	assertEngineHTTP(t, second.URLs["web.http"], "linked")
	replayed, err := engine.Run(context.Background(), RunRequest{Path: linked, RequestID: "retry", Services: []string{"web"}})
	if err != nil || replayed.ID != second.ID {
		t.Fatalf("request replay: %+v %v", replayed, err)
	}
	logs, err := engine.Logs(linked, 4096)
	if err != nil || logs.RunID != second.ID || strings.Contains(logs.Text, "must-not-leak") || strings.Contains(logs.Text, "example.invalid") {
		t.Fatalf("logs: %+v %v", logs, err)
	}
	plan, err := engine.PrunePlan(context.Background(), linked)
	if err != nil || !strings.Contains(strings.Join(plan.Blockers, " "), "Stop") {
		t.Fatalf("running prune plan: %+v %v", plan, err)
	}
	if _, err := engine.Stop(context.Background(), primary); err != nil {
		t.Fatal(err)
	}
	assertEngineHTTP(t, second.URLs["web.http"], "linked")
	startEngineRun(t, engine, primary, "restart")
	first = waitEngineState(t, engine, primary, "running")
	assertEngineHTTP(t, first.URLs["web.http"], "primary")
	contents, err := os.ReadFile(filepath.Join(primary, "output", "count"))
	if err != nil || strings.Count(string(contents), "prepared") != 2 {
		t.Fatalf("setup was skipped on restart: %q %v", contents, err)
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(engine.configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close(context.Background()) }()
	if choices := reopened.Config().Choices[linked]; choices.Target != "local" || len(choices.Services) != 1 {
		t.Fatalf("choices lost on restart: %+v", choices)
	}
	if snapshot, err := reopened.Status(context.Background()); err != nil || len(snapshot.Workspaces) != 2 {
		t.Fatalf("reopened status: %+v %v", snapshot, err)
	}
}

func TestEngineStartupExitReadinessTimeoutCancellationAndPrepare(t *testing.T) {
	engine, config, primary, _ := engineFixture(t)
	if err := os.WriteFile(filepath.Join(primary, "FAIL_START"), []byte("failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	startEngineRun(t, engine, primary, "crash")
	failed := waitEngineState(t, engine, primary, "failed")
	if !strings.Contains(failed.Error, "exited") || time.Since(started) > 5*time.Second {
		t.Fatalf("startup exit waited for timeout: %+v", failed)
	}
	_ = os.Remove(filepath.Join(primary, "FAIL_START"))
	config.Repositories[0].Services[0].Command.Script = "sleep 30"
	config.Repositories[0].Services[0].Readiness.TimeoutSeconds = 1
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	startEngineRun(t, engine, primary, "timeout")
	failed = waitEngineState(t, engine, primary, "failed")
	if !strings.Contains(failed.Error, "ready in time") {
		t.Fatalf("readiness timeout: %+v", failed)
	}
	config.Repositories[0].Setup[0].Script = "sleep 30"
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	startEngineRun(t, engine, primary, "cancel")
	stopped, err := engine.Stop(context.Background(), primary)
	if err != nil || stopped.State != "stopped" {
		t.Fatalf("cancel setup: %+v %v", stopped, err)
	}
	config.Repositories[0].Setup = []Command{{Script: "mkdir -p output; printf prepared > output/ready"}}
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	_, err = engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "prepare", PrepareOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	prepared := waitEngineState(t, engine, primary, "stopped")
	if prepared.Step != "Prepared" || prepared.Error != "" {
		t.Fatalf("prepare completion: %+v", prepared)
	}
}

func TestEarlierServiceExitFailsWhileDependencyStarts(t *testing.T) {
	engine, config, primary, _ := engineFixture(t)
	config.Repositories[0].Services[0].Command.Environment["SWITCHYARD_TEST_SERVICE"] = "exit-soon"
	config.Repositories[0].Services = append(config.Repositories[0].Services, ServiceConfig{
		ID: "slow", Name: "Slow", Dependencies: []string{"web"}, Command: Command{Script: "sleep 30"},
		Ports: []Port{{Name: "http"}}, Readiness: Readiness{Port: "http", TimeoutSeconds: 10},
	})
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := engine.Run(context.Background(), RunRequest{Path: primary, RequestID: "dependencies", Services: []string{"slow"}}); err != nil {
		t.Fatal(err)
	}
	failed := waitEngineState(t, engine, primary, "failed")
	if !strings.Contains(failed.Error, "Web exited") || time.Since(started) > 5*time.Second {
		t.Fatalf("earlier service exit was missed: %+v", failed)
	}
}

func TestRunningServiceHealthRecoversThenReportsPersistentFailure(t *testing.T) {
	engine, config, primary, _ := engineFixture(t)
	config.Repositories[0].Services[0].Readiness.TimeoutSeconds = 2
	if err := engine.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	startEngineRun(t, engine, primary, "health")
	waitEngineState(t, engine, primary, "running")
	marker := filepath.Join(primary, "UNHEALTHY")
	if err := os.WriteFile(marker, []byte("failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	waiting := waitEngineState(t, engine, primary, "starting")
	if !strings.Contains(waiting.Step, "Waiting for Web") {
		t.Fatalf("unavailable service was not visible: %+v", waiting)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	running := waitEngineState(t, engine, primary, "running")
	assertEngineHTTP(t, running.URLs["web.http"], "primary")
	if err := os.WriteFile(marker, []byte("failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	failed := waitEngineState(t, engine, primary, "failed")
	if !strings.Contains(failed.Error, "stopped responding") {
		t.Fatalf("persistent health failure was hidden: %+v", failed)
	}
}

func engineFixture(t *testing.T) (*Engine, Config, string, string) {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	primary, linked := filepath.Join(base, "primary"), filepath.Join(base, "linked")
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	engineGit(t, primary, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(primary, "README"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	engineGit(t, primary, "add", "README")
	engineGit(t, primary, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	engineGit(t, primary, "worktree", "add", "-b", "linked", linked)
	config := Config{SchemaVersion: 1, Repositories: []RepositoryConfig{{ID: "sample", Name: "Sample", Path: primary,
		DefaultTarget: "local", Targets: []Target{{ID: "local", Name: "Local"}},
		Setup: []Command{{Script: "if [ -f FAIL_SETUP ]; then echo setup-prerequisite-missing; exit 2; fi; mkdir -p output; printf prepared > output/ready; echo prepared >> output/count"}, {Script: "printf second > second", Directory: "output"}},
		Services: []ServiceConfig{{ID: "web", Name: "Web", Command: Command{
			Script: shellQuote(os.Args[0]) + " -test.run=^TestRuntimeService$", Environment: map[string]string{"SWITCHYARD_TEST_SERVICE": "server"}},
			Ports: []Port{{Name: "http", Environment: "PORT", URL: true}}, Readiness: Readiness{Port: "http", Path: "/", TimeoutSeconds: 10}}},
	}}}
	path := filepath.Join(base, "settings", "configuration.json")
	if err := SaveConfig(path, config); err != nil {
		t.Fatal(err)
	}
	engine, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = engine.Close(ctx)
	})
	return engine, config, primary, linked
}

func engineGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture Git: %s %v", output, err)
	}
}

func startEngineRun(t *testing.T, engine *Engine, path, id string) Run {
	t.Helper()
	run, err := engine.Run(context.Background(), RunRequest{Path: path, RequestID: id, Services: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func waitEngineState(t *testing.T, engine *Engine, path, state string) Run {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var current Run
	for time.Now().Before(deadline) {
		snapshot, err := engine.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, workspace := range snapshot.Workspaces {
			if workspace.Path == path && workspace.Run != nil {
				current = *workspace.Run
				if current.State == state {
					return current
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs, _ := engine.Logs(path, 4096)
	t.Fatalf("wanted %s; got %+v; logs: %s", state, current, logs.Text)
	return Run{}
}

func assertEngineHTTP(t *testing.T, url, expected string) {
	t.Helper()
	client := http.Client{Timeout: time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 256))
	if err != nil || response.StatusCode != 200 || string(body) != expected {
		t.Fatalf("HTTP response: %q %v", body, err)
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func runTestDaemon() {
	engine, err := New(os.Getenv("SWITCHYARD_TEST_CONFIG"))
	if err != nil {
		os.Exit(10)
	}
	path := engine.Config().Repositories[0].Path
	if _, err := engine.Run(context.Background(), RunRequest{Path: path, RequestID: "daemon", Services: []string{"web"}}); err != nil {
		os.Exit(11)
	}
	for {
		status, err := engine.Status(context.Background())
		if err != nil {
			os.Exit(12)
		}
		for _, workspace := range status.Workspaces {
			if workspace.Path == path && workspace.Run != nil && workspace.Run.State == "running" {
				if err := os.WriteFile(os.Getenv("SWITCHYARD_TEST_READY"), []byte(workspace.Run.URLs["web.http"]+"\n"+workspace.Run.URLs["web.child"]), 0o600); err != nil {
					os.Exit(13)
				}
				select {}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

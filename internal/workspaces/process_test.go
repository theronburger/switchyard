package workspaces

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonDeathStopsServiceAndForkedDescendant(t *testing.T) {
	engine, config, _, _ := engineFixture(t)
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := &config.Repositories[0].Services[0]
	service.Command.Environment["SWITCHYARD_TEST_SERVICE"] = "fork"
	service.Ports = append(service.Ports, Port{Name: "child", Environment: "CHILD_PORT", URL: true})
	if err := SaveConfig(engine.configPath, config); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	daemon := exec.Command(os.Args[0], "--test-daemon")
	daemon.Env = append(os.Environ(), "SWITCHYARD_TEST_CONFIG="+engine.configPath, "SWITCHYARD_TEST_READY="+readyPath)
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	var urls []string
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(readyPath)
		if err == nil {
			urls = strings.Split(string(contents), "\n")
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(urls) != 2 || urls[0] == "" || urls[1] == "" {
		t.Fatal("test daemon did not start both service processes")
	}
	assertEngineHTTP(t, urls[0], "primary")
	assertEngineHTTP(t, urls[1], "primary")
	if err := daemon.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = daemon.Wait()
	client := http.Client{Timeout: 200 * time.Millisecond}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, url := range urls {
			response, err := client.Get(url)
			if err == nil {
				alive = true
				_ = response.Body.Close()
			}
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a service or forked descendant survived daemon termination")
}

func TestGuardianEOFStopsTermIgnoringDescendants(t *testing.T) {
	root := t.TempDir()
	log := &runLog{}
	process, err := launchProcess(context.Background(), processSpec{
		Script: "trap '' TERM; touch ready; sleep 30 & wait", Directory: root,
		Environment: []string{"HOME=" + root, "PATH=/usr/bin:/bin"},
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			process.stop()
			t.Fatal("test command did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	finished := make(chan struct{})
	go func() { process.stop(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("TERM-ignoring process group did not stop")
	}
}

func TestCommandSubstitutionsNeverEnterShellSource(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	command := Command{Script: "printf '%s' '{workspace}'", Environment: map[string]string{
		"WORKSPACE": "{workspace}", "URL": "http://127.0.0.1:{port:web.http}", "INSTANCE": "{instance}",
	}}
	spec, err := commandSpec(command, RepositoryConfig{Path: root}, Target{}, Run{ID: "instance", Path: root}, map[string]int{"web.http": 31000}, nil)
	if err != nil || spec.Script != command.Script {
		t.Fatalf("shell source was rewritten: %+v %v", spec, err)
	}
	joined := strings.Join(spec.Environment, "\n")
	if !strings.Contains(joined, "WORKSPACE="+root) || !strings.Contains(joined, "URL=http://127.0.0.1:31000") {
		t.Fatalf("environment substitutions: %s", joined)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	command.Directory = "linked"
	if _, err := commandSpec(command, RepositoryConfig{Path: root}, Target{}, Run{Path: root}, nil, nil); err == nil {
		t.Fatal("command escaped through a symlinked directory")
	}
}

package marketplacecontrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/control/environment"
)

const preparationHelperMode = "SWITCHYARD_PREPARATION_HELPER_MODE"

func TestOSPreparationRunnerUsesExactArgvControlledEnvironmentAndBoundedLogs(t *testing.T) {
	t.Parallel()
	preparation := helperPreparation(t, "exact")
	preparation.Arguments = append(preparation.Arguments, "one two", "$(foreign)")
	preparation.Environment = append(preparation.Environment, "PLANNED_VALUE=only-this")
	runner := OSPreparationRunner{MaximumLogBytes: 96}
	if err := runner.Run(context.Background(), preparation); err != nil {
		t.Fatal(err)
	}
	stdout := readPreparationLog(t, preparation.RunDirectory, PreparationStdoutLog)
	if !strings.Contains(stdout, "one two|$(foreign)|only-this") {
		t.Fatalf("exact preparation output: %q", stdout)
	}
	if len(stdout) > 96 {
		t.Fatalf("stdout log exceeded its bound: %d", len(stdout))
	}
	for _, name := range []string{PreparationStdoutLog, PreparationStderrLog} {
		info, err := os.Lstat(filepath.Join(preparation.RunDirectory, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("controlled log %s: info=%v err=%v", name, info, err)
		}
	}
}

func TestOSPreparationRunnerRedactsFailureAndRefusesSymlinkLogs(t *testing.T) {
	t.Parallel()
	preparation := helperPreparation(t, "failure")
	preparation.Arguments = append(preparation.Arguments, "secret@example.invalid")
	err := (OSPreparationRunner{}).Run(context.Background(), preparation)
	if !errors.Is(err, ErrPreparationFailed) || strings.Contains(err.Error(), "secret") ||
		strings.Contains(err.Error(), "@") {
		t.Fatalf("preparation failure leaked details: %v", err)
	}

	symlinked := helperPreparation(t, "exact")
	if err := os.MkdirAll(symlinked.RunDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	foreignPath := filepath.Join(t.TempDir(), "foreign.log")
	if err := os.WriteFile(foreignPath, []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignPath, filepath.Join(symlinked.RunDirectory, PreparationStdoutLog)); err != nil {
		t.Fatal(err)
	}
	err = (OSPreparationRunner{}).Run(context.Background(), symlinked)
	if !errors.Is(err, ErrPreparationInvalid) {
		t.Fatalf("symlinked log was accepted: %v", err)
	}
	contents, readErr := os.ReadFile(foreignPath)
	if readErr != nil || string(contents) != "foreign\n" {
		t.Fatalf("foreign log changed: contents=%q err=%v", contents, readErr)
	}
}

func TestOSPreparationRunnerCancelsItsOwnedProcessGroup(t *testing.T) {
	t.Parallel()
	preparation := helperPreparation(t, "parent")
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	preparation.Environment = append(preparation.Environment, "SWITCHYARD_PREPARATION_CHILD_PID="+childPIDPath)
	runner := OSPreparationRunner{
		MaximumLogBytes: 1024,
		GracePeriod:     20 * time.Millisecond,
		KillWait:        time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx, preparation)
	}()
	childPID := waitForPreparationChild(t, childPIDPath)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled preparation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled preparation did not return")
	}
	waitForProcessExit(t, childPID)
}

func TestOSPreparationRunnerEnforcesPreparationTimeout(t *testing.T) {
	t.Parallel()
	preparation := helperPreparation(t, "parent")
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	preparation.Environment = append(preparation.Environment, "SWITCHYARD_PREPARATION_CHILD_PID="+childPIDPath)
	preparation.Timeout = 250 * time.Millisecond
	runner := OSPreparationRunner{
		MaximumLogBytes: 1024,
		GracePeriod:     20 * time.Millisecond,
		KillWait:        time.Second,
	}
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(context.Background(), preparation)
	}()
	childPID := waitForPreparationChild(t, childPIDPath)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timed out preparation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out preparation did not return")
	}
	waitForProcessExit(t, childPID)
}

func TestPreparationHelperProcess(t *testing.T) {
	mode := os.Getenv(preparationHelperMode)
	if mode == "" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	arguments := []string{}
	if separator >= 0 {
		arguments = os.Args[separator+1:]
	}
	switch mode {
	case "exact":
		fmt.Fprint(os.Stdout, strings.Join(append(arguments, os.Getenv("PLANNED_VALUE")), "|"))
		fmt.Fprint(os.Stdout, strings.Repeat("x", 4096))
	case "failure":
		fmt.Fprintln(os.Stderr, strings.Join(arguments, "|"))
		os.Exit(23)
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=TestPreparationHelperProcess")
		child.Env = replaceEnvironment(os.Environ(), preparationHelperMode, "child")
		if err := child.Start(); err != nil {
			os.Exit(24)
		}
		pidPath := os.Getenv("SWITCHYARD_PREPARATION_CHILD_PID")
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(25)
		}
		select {}
	case "child":
		select {}
	default:
		os.Exit(26)
	}
}

func helperPreparation(t *testing.T, mode string) environment.PreparationSpec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	return environment.PreparationSpec{
		ID:         "test.prepare." + mode,
		Executable: executable,
		Arguments:  []string{"-test.run=TestPreparationHelperProcess", "--"},
		Environment: []string{
			"HOME=" + t.TempDir(),
			"PATH=/usr/bin:/bin",
			"TMPDIR=" + t.TempDir(),
			preparationHelperMode + "=" + mode,
		},
		Directory: t.TempDir(), RunDirectory: filepath.Join(t.TempDir(), "preparations", mode),
		Timeout: time.Minute,
	}
}

func readPreparationLog(t *testing.T, directory, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment))
	for _, variable := range environment {
		if !strings.HasPrefix(variable, prefix) {
			result = append(result, variable)
		}
	}
	return append(result, prefix+value)
}

func waitForPreparationChild(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(contents))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("preparation child did not start")
	return 0
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("preparation child %d survived group cancellation", pid)
}

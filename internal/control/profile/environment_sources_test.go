package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/theronburger/switchyard/internal/configuration"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

func TestParseDotenvReadsDataWithoutExpansionOrExecution(t *testing.T) {
	entries, err := parseDotenv([]byte(strings.Join([]string{
		"# comment",
		"",
		"PLAIN=value with spaces",
		"export EXPORTED=yes",
		"  SPACED = padded  ",
		"SINGLE='raw $HOME `id` \\n'",
		"DOUBLE=\"escaped\\tvalue $(id)\"",
		"EMPTY=",
		"WINDOWS=line\r",
		"HASH_INSIDE=a#b",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"PLAIN": "value with spaces", "EXPORTED": "yes", "SPACED": "padded",
		"SINGLE": "raw $HOME `id` \\n", "DOUBLE": "escaped\tvalue $(id)", "EMPTY": "",
		"WINDOWS": "line", "HASH_INSIDE": "a#b",
	}
	if len(entries) != len(expected) {
		t.Fatalf("entries: %+v", entries)
	}
	for name, value := range expected {
		if entries[name] != value {
			t.Fatalf("%s: got %q want %q", name, entries[name], value)
		}
	}
}

func TestParseDotenvRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"nul byte":              "A=b\x00c",
		"not an assignment":     "just words",
		"command line":          "rm -rf /",
		"invalid name":          "1BAD=x",
		"name with dash":        "BAD-NAME=x",
		"duplicate":             "A=1\nA=2",
		"unterminated single":   "A='open",
		"unterminated double":   "A=\"open",
		"trailing after quote":  "A='x' && id",
		"embedded single quote": "A='x'y'",
		"stray quote":           "A=x\"y",
		"over-long line":        "A=" + strings.Repeat("x", maximumEnvironmentSourceLine),
	}
	for name, contents := range cases {
		_, err := parseDotenv([]byte(contents))
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		if strings.Contains(err.Error(), "rm -rf") || strings.Contains(err.Error(), "xxxx") {
			t.Fatalf("%s: error echoed file content: %v", name, err)
		}
	}
	var tooMany strings.Builder
	for index := 0; index <= maximumEnvironmentSourceEntries; index++ {
		tooMany.WriteString("K_" + strconv.Itoa(index) + "=v\n")
	}
	if _, err := parseDotenv([]byte(tooMany.String())); err == nil {
		t.Fatal("entry count bound was not enforced")
	}
}

func TestReadEnvironmentSourcesAppliesAllowlistAndTargets(t *testing.T) {
	registration := environmentSourceRegistration(t)
	writeWorktreeFile(t, registration.WorktreeRoot, ".env", "ALLOWED=one\nSECRET_NOT_ALLOWED=hidden\nSTAGING_ONLY=ignored\n")
	writeWorktreeFile(t, registration.WorktreeRoot, "config/staging.env", "STAGING_ONLY=two\n")
	registration.Profile.EnvironmentSources = map[string]configuration.EnvironmentSource{
		"base":    {Kind: "dotenv", Root: "worktree", Path: ".env", Allow: []string{"ALLOWED", "ABSENT"}},
		"staging": {Kind: "dotenv", Root: "worktree", Path: "config/staging.env", Targets: []string{"staging"}, Allow: []string{"STAGING_ONLY"}},
	}
	local, err := ReadEnvironmentSources(registration, "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 1 || local["ALLOWED"] != "one" {
		t.Fatalf("local sources: %+v", local)
	}
	staging, err := ReadEnvironmentSources(registration, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(staging) != 2 || staging["ALLOWED"] != "one" || staging["STAGING_ONLY"] != "two" {
		t.Fatalf("staging sources: %+v", staging)
	}
	if _, err := ReadEnvironmentSources(registration, "missing"); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("unknown target: %v", err)
	}
}

func TestReadEnvironmentSourcesFailurePaths(t *testing.T) {
	registration := environmentSourceRegistration(t)
	worktree := registration.WorktreeRoot
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.env"), []byte("ALLOWED=escaped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.env"), filepath.Join(worktree, "link.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(worktree, "linkdir")); err != nil {
		t.Fatal(err)
	}
	writeWorktreeFile(t, worktree, "big.env", "ALLOWED="+strings.Repeat("x", maximumEnvironmentSourceBytes)+"\n")
	writeWorktreeFile(t, worktree, "broken.env", "ALLOWED='unterminated\n")
	writeWorktreeFile(t, worktree, "secret.env", "ALLOWED=hunter2-secret-value\n")
	if err := os.Mkdir(filepath.Join(worktree, "dir.env"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]configuration.EnvironmentSource{
		"missing required":   {Kind: "dotenv", Root: "worktree", Path: "absent.env", Allow: []string{"ALLOWED"}},
		"symlinked file":     {Kind: "dotenv", Root: "worktree", Path: "link.env", Allow: []string{"ALLOWED"}},
		"optional symlink":   {Kind: "dotenv", Root: "worktree", Path: "link.env", Optional: true, Allow: []string{"ALLOWED"}},
		"symlinked dir":      {Kind: "dotenv", Root: "worktree", Path: "linkdir/outside.env", Allow: []string{"ALLOWED"}},
		"parent escape":      {Kind: "dotenv", Root: "worktree", Path: "../" + filepath.Base(outside) + "/outside.env", Allow: []string{"ALLOWED"}},
		"absolute path":      {Kind: "dotenv", Root: "worktree", Path: filepath.Join(outside, "outside.env"), Allow: []string{"ALLOWED"}},
		"directory":          {Kind: "dotenv", Root: "worktree", Path: "dir.env", Allow: []string{"ALLOWED"}},
		"oversized":          {Kind: "dotenv", Root: "worktree", Path: "big.env", Allow: []string{"ALLOWED"}},
		"malformed":          {Kind: "dotenv", Root: "worktree", Path: "broken.env", Allow: []string{"ALLOWED"}},
		"unsupported kind":   {Kind: "shell", Root: "worktree", Path: "secret.env", Allow: []string{"ALLOWED"}},
		"unsupported root":   {Kind: "dotenv", Root: "runtime", Path: "secret.env", Allow: []string{"ALLOWED"}},
		"loader variable":    {Kind: "dotenv", Root: "worktree", Path: "secret.env", Allow: []string{"DYLD_INSERT_LIBRARIES"}},
		"trusted base":       {Kind: "dotenv", Root: "worktree", Path: "secret.env", Allow: []string{"PATH"}},
		"switchyard prefix":  {Kind: "dotenv", Root: "worktree", Path: "secret.env", Allow: []string{"SWITCHYARD_TOKEN"}},
		"shell startup hook": {Kind: "dotenv", Root: "worktree", Path: "secret.env", Allow: []string{"BASH_ENV"}},
	}
	for name, source := range cases {
		registration.Profile.EnvironmentSources = map[string]configuration.EnvironmentSource{"probe": source}
		_, err := ReadEnvironmentSources(registration, "local")
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		if strings.Contains(err.Error(), "escaped") || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "unterminated\n") {
			t.Fatalf("%s: error leaked file content: %v", name, err)
		}
	}
	registration.Profile.EnvironmentSources = map[string]configuration.EnvironmentSource{
		"optional": {Kind: "dotenv", Root: "worktree", Path: "absent.env", Optional: true, Allow: []string{"ALLOWED"}},
	}
	if values, err := ReadEnvironmentSources(registration, "local"); err != nil || len(values) != 0 {
		t.Fatalf("optional missing source: %v %+v", err, values)
	}
	registration.Profile.EnvironmentSources = map[string]configuration.EnvironmentSource{
		"a": {Kind: "dotenv", Root: "worktree", Path: "secret.env", Allow: []string{"ALLOWED"}},
		"b": {Kind: "dotenv", Root: "worktree", Path: "secret.env", Allow: []string{"ALLOWED"}},
	}
	if _, err := ReadEnvironmentSources(registration, "local"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("duplicate allow across sources: %v", err)
	}
}

func TestPlanBuilderLayersEnvironmentSourcesBelowTargetAndService(t *testing.T) {
	registration := environmentSourceRegistration(t)
	writeWorktreeFile(t, registration.WorktreeRoot, ".env", strings.Join([]string{
		"FROM_SOURCE=source",
		"TARGET_WINS=source",
		"SERVICE_WINS=source",
		"MODE=source",
		"NOT_ALLOWED=leak",
	}, "\n"))
	registration.Profile.EnvironmentSources = map[string]configuration.EnvironmentSource{
		"base": {Kind: "dotenv", Root: "worktree", Path: ".env", Allow: []string{"FROM_SOURCE", "TARGET_WINS", "SERVICE_WINS", "MODE"}},
	}
	literal := func(value string) configuration.ValueRef { return configuration.ValueRef{Literal: &value} }
	target := registration.Profile.Targets["local"]
	target.Environment["TARGET_WINS"] = literal("target")
	target.Environment["SERVICE_WINS"] = literal("target")
	registration.Profile.Targets["local"] = target
	service := registration.Profile.Services["web"]
	service.Environment["SERVICE_WINS"] = literal("service")
	service.Prepare = []configuration.Command{{Executable: "/usr/bin/true", WorkingDirectory: ".", Timeout: "30s", Environment: map[string]configuration.ValueRef{"PREPARE_ONLY": literal("yes")}}}
	registration.Profile.Services["web"] = service
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	lease := portlease.Lease{Key: portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}
	plan, err := NewPlanBuilder(registry).Build(environmentcontrol.PlanningRequest{
		EnvironmentID: registration.EnvironmentID, RunID: "run_01",
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{lease},
	})
	if err != nil {
		t.Fatal(err)
	}
	expect := func(environment []string, want map[string]string, forbidden ...string) {
		t.Helper()
		seen := make(map[string]int)
		for _, entry := range environment {
			name, value, _ := strings.Cut(entry, "=")
			seen[name]++
			if expected, checked := want[name]; checked && value != expected {
				t.Fatalf("%s: got %q want %q in %v", name, value, expected, environment)
			}
		}
		for name := range want {
			if seen[name] != 1 {
				t.Fatalf("%s appeared %d times in %v", name, seen[name], environment)
			}
		}
		for _, name := range forbidden {
			if seen[name] != 0 {
				t.Fatalf("%s leaked into %v", name, environment)
			}
		}
	}
	expected := map[string]string{
		"FROM_SOURCE": "source", "TARGET_WINS": "target", "SERVICE_WINS": "service", "MODE": "test",
		"HOME": registration.HomeDirectory, "PATH": registration.ExecutablePath, "TMPDIR": registration.TemporaryDirectory,
	}
	expect(plan.ServiceStages[0][0].Process.Environment, expected, "NOT_ALLOWED", "PREPARE_ONLY")
	if len(plan.Preparations) != 1 {
		t.Fatalf("preparations: %+v", plan.Preparations)
	}
	prepared := map[string]string{"PREPARE_ONLY": "yes"}
	for name, value := range expected {
		prepared[name] = value
	}
	expect(plan.Preparations[0].Environment, prepared, "NOT_ALLOWED")
}

func TestPlanBuilderRefusesUnreadableRequiredEnvironmentSource(t *testing.T) {
	registration := environmentSourceRegistration(t)
	registration.Profile.EnvironmentSources = map[string]configuration.EnvironmentSource{
		"base": {Kind: "dotenv", Root: "worktree", Path: ".env", Allow: []string{"FROM_SOURCE"}},
	}
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	lease := portlease.Lease{Key: portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}
	request := environmentcontrol.PlanningRequest{
		EnvironmentID: registration.EnvironmentID, RunID: "run_01",
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{lease},
	}
	if _, err := NewPlanBuilder(registry).Build(request); err == nil {
		t.Fatal("plan compiled without its required environment source")
	}
	writeWorktreeFile(t, registration.WorktreeRoot, ".env", "FROM_SOURCE=ok\n")
	if _, err := NewPlanBuilder(registry).Build(request); err != nil {
		t.Fatal(err)
	}
}

func environmentSourceRegistration(t *testing.T) Registration {
	t.Helper()
	registration := profileRegistration(t)
	registration.Profile.Targets["staging"] = configuration.Target{DisplayName: "Staging", Risk: "local", Environment: map[string]configuration.ValueRef{}}
	return registration
}

func writeWorktreeFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

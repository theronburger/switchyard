package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

type diagnosticOperationStore struct {
	operation contractv2.Operation
	err       error
}

func (store diagnosticOperationStore) ReadOperation(context.Context, string) (contractv2.Operation, error) {
	return store.operation, store.err
}

// testRunRoots lays environment runs out exactly as the plan builder does:
// beneath repositories/<profileKey>/<worktreeID>/environments/<envID>/runs.
func testRunRoots(runtimeRoot string, environmentIDs ...string) EnvironmentRunRootMap {
	roots := make(EnvironmentRunRootMap, len(environmentIDs))
	for _, environmentID := range environmentIDs {
		roots[environmentID] = filepath.Join(runtimeRoot, "repositories", "sample", "wt_01", "environments", environmentID, "runs")
	}
	return roots
}

func TestOperationDiagnosticsReaderReturnsBoundedRedactedOwnedLogs(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	logReference := "run_01/preparations/billing-service/command-0"
	runRoots := testRunRoots(runtimeRoot, "env_01")
	logDirectory := filepath.Join(runRoots["env_01"], filepath.FromSlash(logReference))
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stdout := strings.Repeat("compiler context\n", 30) +
		"src/utils/importFoundation.ts(43,43): error TS2304: Cannot find name 'ManagedImportIndexDefinition'.\n"
	stderr := strings.Join([]string{
		"preparation command (/private/tool --token visible)",
		"DATABASE_URL=postgres://secret",
		"token=visible",
		"failure under /Users/person/private for person@example.com",
	}, "\n")
	for name, contents := range map[string]string{"stdout.log": stdout, "stderr.log": stderr} {
		if err := os.WriteFile(filepath.Join(logDirectory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_01", EnvironmentID: "env_01",
		Error: &contractv2.ContractError{LogReference: logReference},
	}}, runtimeRoot, runRoots)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := reader.ReadOperationDiagnostics(context.Background(), "operation_01", 256)
	if err != nil || len(diagnostics.Excerpts) != 2 || diagnostics.OperationID != "operation_01" {
		t.Fatalf("diagnostics: %+v err=%v", diagnostics, err)
	}
	encoded := diagnostics.Excerpts[0].Content + diagnostics.Excerpts[1].Content
	if !strings.Contains(encoded, "TS2304") || !diagnostics.Excerpts[0].Truncated || !diagnostics.Excerpts[1].Redacted {
		t.Fatalf("expected bounded compiler diagnostic and redaction: %+v", diagnostics.Excerpts)
	}
	for _, forbidden := range []string{"--token visible", "postgres://secret", "token=visible", "/Users/person", "person@example.com"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("diagnostics leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestOperationDiagnosticsReaderRefusesUnownedOrInvalidPaths(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	logReference := "run_01/preparations/service/command-0"
	runRoots := testRunRoots(runtimeRoot, "env_01")
	logDirectory := filepath.Join(runRoots["env_01"], filepath.FromSlash(logReference))
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign.log")
	if err := os.WriteFile(foreign, []byte("foreign secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(logDirectory, "stderr.log")); err != nil {
		t.Fatal(err)
	}
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_01", EnvironmentID: "env_01",
		Error: &contractv2.ContractError{LogReference: logReference},
	}}, runtimeRoot, runRoots)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_01", 0); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("symlink diagnostics error: %v", err)
	}
	reader.store = diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_01", EnvironmentID: "env_01",
		Error: &contractv2.ContractError{LogReference: "../../foreign"},
	}}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_01", 0); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("traversal diagnostics error: %v", err)
	}
}

func TestOperationDiagnosticsReaderDistinguishesMissingOperationFromStoreFailure(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{err: state.ErrOperationNotFound}, runtimeRoot, EnvironmentRunRootMap{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_01", 0); !errors.Is(err, ErrOperationDiagnosticsNotFound) {
		t.Fatalf("missing operation error: %v", err)
	}
	privateFailure := errors.New("private database failure")
	reader.store = diagnosticOperationStore{err: privateFailure}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_01", 0); !errors.Is(err, privateFailure) {
		t.Fatalf("store failure: %v", err)
	}
}

func TestOperationDiagnosticsReaderResolvesProfileActionLogs(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	logReference := "sample/operation_09"
	logDirectory := filepath.Join(runtimeRoot, "actions", filepath.FromSlash(logReference))
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDirectory, "stderr.log"), []byte("lint failed: token=visible\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An environment-scoped action's diagnostics come from the actions root,
	// not from the environment run directory.
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_09", Kind: ProfileActionOperationKind, EnvironmentID: "env_01",
		Error: &contractv2.ContractError{LogReference: logReference},
	}}, runtimeRoot, testRunRoots(runtimeRoot, "env_01"))
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := reader.ReadOperationDiagnostics(context.Background(), "operation_09", 256)
	if err != nil || len(diagnostics.Excerpts) != 1 || !strings.Contains(diagnostics.Excerpts[0].Content, "lint failed") ||
		strings.Contains(diagnostics.Excerpts[0].Content, "token=visible") {
		t.Fatalf("action diagnostics: %+v err=%v", diagnostics, err)
	}
	// The same reference on a non-action operation must not escape into the actions root.
	other, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_10", Kind: "environment.start", EnvironmentID: "env_01",
		Error: &contractv2.ContractError{LogReference: "../../actions/" + logReference},
	}}, runtimeRoot, testRunRoots(runtimeRoot, "env_01"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ReadOperationDiagnostics(context.Background(), "operation_10", 256); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("traversal into the actions root was allowed: %v", err)
	}
}

func TestOperationDiagnosticsReaderResolvesProfileActionLogsWithoutEnvironment(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	logReference := "sample/operation_11"
	logDirectory := filepath.Join(runtimeRoot, "actions", filepath.FromSlash(logReference))
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDirectory, "stdout.log"), []byte("tidy: 3 files changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Repository, worktree, and machine scoped actions never carry an environment.
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_11", Kind: ProfileActionOperationKind,
		Error: &contractv2.ContractError{LogReference: logReference},
	}}, runtimeRoot, testRunRoots(runtimeRoot, "env_01"))
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := reader.ReadOperationDiagnostics(context.Background(), "operation_11", 256)
	if err != nil || len(diagnostics.Excerpts) != 1 || !strings.Contains(diagnostics.Excerpts[0].Content, "3 files changed") ||
		diagnostics.EnvironmentID != "" || diagnostics.OperationID != "operation_11" {
		t.Fatalf("non-environment action diagnostics: %+v err=%v", diagnostics, err)
	}
	// Any other operation kind still needs an environment to locate its logs.
	reader.store = diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_12", Kind: "environment.start",
		Error: &contractv2.ContractError{LogReference: "run_01/preparations/service/command-0"},
	}}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_12", 256); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("environment operation without environment was served: %v", err)
	}
	// A traversal reference on an environment-less action stays inside the actions root.
	reader.store = diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_13", Kind: ProfileActionOperationKind,
		Error: &contractv2.ContractError{LogReference: "../environments/env_01/runs/run_01"},
	}}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_13", 256); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("traversal out of the actions root was allowed: %v", err)
	}
}

func TestOperationDiagnosticsReaderRefusesForeignProfileActionLogs(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	foreignRoot := t.TempDir()
	foreignLog := filepath.Join(foreignRoot, "stdout.log")
	if err := os.WriteFile(foreignLog, []byte("foreign secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runtimeRoot, "actions", "sample"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A symlinked log file inside an owned action directory.
	ownedDirectory := filepath.Join(runtimeRoot, "actions", "sample", "operation_20")
	if err := os.Mkdir(ownedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignLog, filepath.Join(ownedDirectory, "stdout.log")); err != nil {
		t.Fatal(err)
	}
	// A symlinked action directory pointing outside the runtime root.
	if err := os.Symlink(foreignRoot, filepath.Join(runtimeRoot, "actions", "sample", "operation_21")); err != nil {
		t.Fatal(err)
	}
	// A symlinked profile directory above the action directory.
	if err := os.Symlink(foreignRoot, filepath.Join(runtimeRoot, "actions", "linked")); err != nil {
		t.Fatal(err)
	}
	// A group-readable action directory is not private.
	sharedDirectory := filepath.Join(runtimeRoot, "actions", "sample", "operation_22")
	if err := os.Mkdir(sharedDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDirectory, "stdout.log"), []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A group-readable log inside a private directory.
	leakyDirectory := filepath.Join(runtimeRoot, "actions", "sample", "operation_23")
	if err := os.Mkdir(leakyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leakyDirectory, "stdout.log"), []byte("leaky"), 0o640); err != nil {
		t.Fatal(err)
	}
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{}, runtimeRoot, EnvironmentRunRootMap{})
	if err != nil {
		t.Fatal(err)
	}
	for name, reference := range map[string]string{
		"symlinked log":        "sample/operation_20",
		"symlinked action dir": "sample/operation_21",
		"symlinked profile":    "linked/operation_20",
		"shared directory":     "sample/operation_22",
		"shared log":           "sample/operation_23",
		"missing":              "sample/operation_24",
	} {
		reader.store = diagnosticOperationStore{operation: contractv2.Operation{
			ID: "operation", Kind: ProfileActionOperationKind,
			Error: &contractv2.ContractError{LogReference: reference},
		}}
		if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation", 256); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
			t.Fatalf("%s: expected ErrOperationDiagnosticsUnavailable, got %v", name, err)
		}
	}
}

// TestRedactDiagnosticLogRemovesRealisticSecretShapes feeds the redactor the
// shapes real child output takes: headers with a scheme, assignments behind
// timestamps and stream prefixes, quoted values, embedded key names, and URI
// userinfo. None of the secret bytes may survive, and the result must not
// claim redaction while leaking.
func TestRedactDiagnosticLogRemovesRealisticSecretShapes(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-REALTOKENVALUE", "ghp_realvalue", "wJalrXUtnFEMI", "sk_live_51Example", "p4ssw0rd",
		"hunter2", "AKIAEXAMPLE", "xoxb-slackvalue", "-----BEGIN",
	}
	input := strings.Join([]string{
		"> Authorization: Bearer sk-ant-api03-REALTOKENVALUE",
		"2026-08-20 12:00:00 INFO  loaded GITHUB_TOKEN=ghp_realvalue",
		"[worker-1] AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI",
		"  STRIPE_KEY: 'sk_live_51Example',",
		`psql: connection string "postgresql://u:p4ssw0rd@h:5432/d"`,
		"passwd=hunter2 retry=3",
		"config.awsAccessKeyId = AKIAEXAMPLE",
		"slack token:xoxb-slackvalue",
		"privateKey=-----BEGIN RSA PRIVATE KEY-----",
		"ordinary progress line 42/100",
	}, "\n")
	output, redacted := redactDiagnosticLog(input)
	if !redacted {
		t.Fatal("secret-bearing output was not marked redacted")
	}
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Errorf("secret %q survived redaction:\n%s", secret, output)
		}
	}
	if !strings.Contains(output, "ordinary progress line 42/100") {
		t.Fatalf("ordinary line was damaged:\n%s", output)
	}
	if !strings.Contains(output, "postgresql://[redacted]@h:5432/d") {
		t.Fatalf("URI userinfo was not redacted whole:\n%s", output)
	}
}

// A readiness timeout on an environment start references the failed
// service's own run directory. The reader must resolve that reference
// beneath the run root the plan builder launched under, and must refuse
// it for an environment it does not configure or a run root that leaves the
// runtime tree.
func TestOperationDiagnosticsReaderResolvesServiceReadinessLogsBeneathEnvironmentRunRoot(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	runRoots := testRunRoots(runtimeRoot, "env_01")
	logReference := "run_01/services/web"
	logDirectory := filepath.Join(runRoots["env_01"], filepath.FromSlash(logReference))
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stderr := "listen tcp 127.0.0.1:43121: bind: address already in use\nDATABASE_URL=postgres://user:secret@localhost/db\n"
	if err := os.WriteFile(filepath.Join(logDirectory, "stderr.log"), []byte(stderr), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := contractv2.Operation{
		ID: "operation_30", Kind: "environment.start", EnvironmentID: "env_01",
		Error: &contractv2.ContractError{
			Code: "ENVIRONMENT_SERVICE_READINESS_TIMED_OUT", LogReference: logReference,
			NextAction: "inspect_operation_diagnostics",
		},
	}
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: operation}, runtimeRoot, runRoots)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := reader.ReadOperationDiagnostics(context.Background(), "operation_30", 0)
	if err != nil || len(diagnostics.Excerpts) != 1 || diagnostics.Excerpts[0].Stream != "stderr" ||
		diagnostics.LogReference != logReference || diagnostics.EnvironmentID != "env_01" {
		t.Fatalf("service diagnostics: %+v err=%v", diagnostics, err)
	}
	content := diagnostics.Excerpts[0].Content
	if !strings.Contains(content, "address already in use") || strings.Contains(content, "secret") || !diagnostics.Excerpts[0].Redacted {
		t.Fatalf("service excerpt not bounded and redacted: %q", content)
	}

	// The old flat layout is not consulted: the same reference with no
	// configured run root is unavailable rather than guessed.
	unconfigured, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: operation}, runtimeRoot, EnvironmentRunRootMap{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unconfigured.ReadOperationDiagnostics(context.Background(), "operation_30", 0); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("unconfigured environment was served: %v", err)
	}

	// A run root outside the runtime tree is refused even when configured.
	foreignDirectory := filepath.Join(t.TempDir(), "runs", "run_01", "services", "web")
	if err := os.MkdirAll(foreignDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignDirectory, "stderr.log"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	escaped, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: operation}, runtimeRoot,
		EnvironmentRunRootMap{"env_01": filepath.Dir(filepath.Dir(filepath.Dir(foreignDirectory)))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.ReadOperationDiagnostics(context.Background(), "operation_30", 0); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("run root outside the runtime tree was served: %v", err)
	}

	// A reference that climbs out of the run root toward another environment is refused.
	reader.store = diagnosticOperationStore{operation: contractv2.Operation{
		ID: "operation_31", Kind: "environment.start", EnvironmentID: "env_01",
		Error: &contractv2.ContractError{LogReference: "../../env_02/runs/run_01/services/web"},
	}}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_31", 0); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("traversal out of the environment run root was served: %v", err)
	}
}

func TestNewOperationDiagnosticsReaderRequiresRunRoots(t *testing.T) {
	if _, err := NewOperationDiagnosticsReader(diagnosticOperationStore{}, filepath.Join(t.TempDir(), "runtime"), nil); !errors.Is(err, ErrOperationDiagnosticsInvalid) {
		t.Fatalf("nil run roots accepted: %v", err)
	}
}

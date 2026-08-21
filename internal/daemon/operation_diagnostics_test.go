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

func TestOperationDiagnosticsReaderReturnsBoundedRedactedOwnedLogs(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	logReference := "run_01/preparations/billing-service/command-0"
	logDirectory := filepath.Join(runtimeRoot, "environments", "env_01", "runs", filepath.FromSlash(logReference))
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
	}}, runtimeRoot)
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
	logDirectory := filepath.Join(runtimeRoot, "environments", "env_01", "runs", filepath.FromSlash(logReference))
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
	}}, runtimeRoot)
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
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{err: state.ErrOperationNotFound}, runtimeRoot)
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
	}}, runtimeRoot)
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
	}}, runtimeRoot)
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
	}}, runtimeRoot)
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
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{}, runtimeRoot)
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

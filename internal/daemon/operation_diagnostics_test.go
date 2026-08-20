package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/state"
)

type diagnosticOperationStore struct {
	operation contractv1.Operation
	err       error
}

func (store diagnosticOperationStore) ReadOperation(context.Context, string) (contractv1.Operation, error) {
	return store.operation, store.err
}

func TestOperationDiagnosticsReaderReturnsBoundedRedactedOwnedLogs(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	logReference := "run_01/preparations/nonprofit-service/command-0"
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
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv1.Operation{
		ID: "operation_01", EnvironmentID: "env_01",
		Error: &contractv1.ContractError{LogReference: logReference},
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
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv1.Operation{
		ID: "operation_01", EnvironmentID: "env_01",
		Error: &contractv1.ContractError{LogReference: logReference},
	}}, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadOperationDiagnostics(context.Background(), "operation_01", 0); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("symlink diagnostics error: %v", err)
	}
	reader.store = diagnosticOperationStore{operation: contractv1.Operation{
		ID: "operation_01", EnvironmentID: "env_01",
		Error: &contractv1.ContractError{LogReference: "../../foreign"},
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
	reader, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv1.Operation{
		ID: "operation_09", Kind: ProfileActionOperationKind, EnvironmentID: "env_01",
		Error: &contractv1.ContractError{LogReference: logReference},
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
	other, err := NewOperationDiagnosticsReader(diagnosticOperationStore{operation: contractv1.Operation{
		ID: "operation_10", Kind: "environment.start", EnvironmentID: "env_01",
		Error: &contractv1.ContractError{LogReference: "../../actions/" + logReference},
	}}, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ReadOperationDiagnostics(context.Background(), "operation_10", 256); !errors.Is(err, ErrOperationDiagnosticsUnavailable) {
		t.Fatalf("traversal into the actions root was allowed: %v", err)
	}
}

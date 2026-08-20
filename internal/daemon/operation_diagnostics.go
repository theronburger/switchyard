package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

const (
	DefaultOperationDiagnosticBytes = 8 * 1024
	MaximumOperationDiagnosticBytes = 32 * 1024
)

var (
	ErrOperationDiagnosticsInvalid     = errors.New("operation diagnostics request is invalid")
	ErrOperationDiagnosticsNotFound    = errors.New("operation diagnostics operation was not found")
	ErrOperationDiagnosticsUnavailable = errors.New("operation has no available diagnostics")

	logEnvironmentAssignment = regexp.MustCompile(`(?m)^\s*[A-Za-z_][A-Za-z0-9_]*=.*$`)
	logSensitiveValue        = regexp.MustCompile(`(?i)\b(authorization|cookie|token|secret|password|api[_-]?key|access[_-]?key)\s*[:=]\s*\S+`)
	logUserPath              = regexp.MustCompile(`/Users/[^/\s]+`)
	logEmail                 = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
)

type OperationDiagnosticsStore interface {
	ReadOperation(context.Context, string) (contractv2.Operation, error)
}

type OperationDiagnosticsReader struct {
	store       OperationDiagnosticsStore
	runtimeRoot string
}

func NewOperationDiagnosticsReader(store OperationDiagnosticsStore, runtimeRoot string) (*OperationDiagnosticsReader, error) {
	if store == nil || !cleanAbsolutePath(runtimeRoot) {
		return nil, ErrOperationDiagnosticsInvalid
	}
	return &OperationDiagnosticsReader{store: store, runtimeRoot: runtimeRoot}, nil
}

func (reader *OperationDiagnosticsReader) ReadOperationDiagnostics(
	ctx context.Context,
	operationID string,
	maximumBytes int,
) (contractv2.OperationDiagnostics, error) {
	if operationID == "" || strings.ContainsAny(operationID, "/\\\x00") {
		return contractv2.OperationDiagnostics{}, ErrOperationDiagnosticsInvalid
	}
	if maximumBytes == 0 {
		maximumBytes = DefaultOperationDiagnosticBytes
	}
	if maximumBytes < 256 || maximumBytes > MaximumOperationDiagnosticBytes {
		return contractv2.OperationDiagnostics{}, ErrOperationDiagnosticsInvalid
	}
	operation, err := reader.store.ReadOperation(ctx, operationID)
	if errors.Is(err, state.ErrOperationNotFound) {
		return contractv2.OperationDiagnostics{}, ErrOperationDiagnosticsNotFound
	}
	if err != nil {
		return contractv2.OperationDiagnostics{}, err
	}
	if operation.Error == nil || operation.Error.LogReference == "" {
		return contractv2.OperationDiagnostics{}, ErrOperationDiagnosticsUnavailable
	}
	// Repository, worktree, and machine scoped profile actions have no
	// environment; every other operation's logs live under one.
	if operation.EnvironmentID == "" && operation.Kind != ProfileActionOperationKind {
		return contractv2.OperationDiagnostics{}, ErrOperationDiagnosticsUnavailable
	}
	logDirectory, valid := reader.logDirectory(operation.Kind, operation.EnvironmentID, operation.Error.LogReference)
	if !valid || !ownedLogDirectory(reader.runtimeRoot, logDirectory) {
		return contractv2.OperationDiagnostics{}, ErrOperationDiagnosticsUnavailable
	}
	diagnostics := contractv2.OperationDiagnostics{
		SchemaVersion: contractv2.SchemaVersion, OperationID: operation.ID,
		EnvironmentID: operation.EnvironmentID, LogReference: operation.Error.LogReference,
		Excerpts: make([]contractv2.OperationLogExcerpt, 0, 2),
	}
	for _, stream := range []string{"stdout", "stderr"} {
		excerpt, found := readOwnedLogExcerpt(filepath.Join(logDirectory, stream+".log"), stream, maximumBytes)
		if found {
			diagnostics.Excerpts = append(diagnostics.Excerpts, excerpt)
		}
	}
	if len(diagnostics.Excerpts) == 0 {
		return contractv2.OperationDiagnostics{}, ErrOperationDiagnosticsUnavailable
	}
	return diagnostics, nil
}

func (reader *OperationDiagnosticsReader) logDirectory(kind, environmentID, reference string) (string, bool) {
	if strings.ContainsRune(reference, 0) || filepath.IsAbs(reference) {
		return "", false
	}
	cleanReference := filepath.Clean(filepath.FromSlash(reference))
	if cleanReference == "." || cleanReference == ".." || strings.HasPrefix(cleanReference, ".."+string(filepath.Separator)) {
		return "", false
	}
	var directory string
	if kind == ProfileActionOperationKind {
		// Command actions log under the private actions root, keyed by profile
		// and operation, independent of any environment run directory.
		directory = filepath.Join(reader.runtimeRoot, "actions", cleanReference)
	} else {
		if environmentID == "" || strings.ContainsAny(environmentID, "/\\\x00") || environmentID == "." || environmentID == ".." {
			return "", false
		}
		directory = filepath.Join(reader.runtimeRoot, "environments", environmentID, "runs", cleanReference)
	}
	return directory, pathContainedBy(reader.runtimeRoot, directory)
}

func readOwnedLogExcerpt(path, stream string, maximumBytes int) (contractv2.OperationLogExcerpt, bool) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return contractv2.OperationLogExcerpt{}, false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	stat, statOK := infoSyscall(info)
	if err != nil || !info.Mode().IsRegular() || !statOK || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return contractv2.OperationLogExcerpt{}, false
	}
	start := info.Size() - int64(maximumBytes)
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	contents := make([]byte, info.Size()-start)
	read, err := file.ReadAt(contents, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return contractv2.OperationLogExcerpt{}, false
	}
	text := strings.ToValidUTF8(string(contents[:read]), "?")
	text, redacted := redactDiagnosticLog(text)
	return contractv2.OperationLogExcerpt{
		Stream: stream, Content: text, Truncated: truncated, Redacted: redacted,
	}, true
}

func ownedLogDirectory(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	info, err := os.Stat(path)
	stat, statOK := infoSyscall(info)
	return err == nil && info.IsDir() && statOK && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}

func redactDiagnosticLog(contents string) (string, bool) {
	redacted := false
	lines := strings.Split(contents, "\n")
	for index, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, " command (") || strings.HasPrefix(strings.TrimSpace(line), "$ ") {
			lines[index] = "[command line omitted]"
			redacted = true
			continue
		}
		if logEnvironmentAssignment.MatchString(line) {
			lines[index] = "[environment assignment omitted]"
			redacted = true
			continue
		}
		updated := logSensitiveValue.ReplaceAllString(line, "$1=[redacted]")
		updated = logUserPath.ReplaceAllString(updated, "/Users/[redacted]")
		updated = logEmail.ReplaceAllString(updated, "[redacted-email]")
		if updated != line {
			redacted = true
			lines[index] = updated
		}
	}
	return strings.Join(lines, "\n"), redacted
}

func infoSyscall(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && strings.IndexByte(path, 0) < 0
}

func pathContainedBy(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

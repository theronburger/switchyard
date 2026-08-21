package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maximumGitOutputBytes = 4 * 1024 * 1024

var (
	ErrManagedConfig       = errors.New("managed workspace configuration is invalid")
	ErrManagedRequest      = errors.New("managed workspace request is invalid")
	ErrManagedExists       = errors.New("managed workspace already exists")
	ErrManagedForeign      = errors.New("workspace is not positively owned by Switchyard")
	ErrManagedDirty        = errors.New("managed workspace has local changes")
	ErrManagedUnpushed     = errors.New("managed workspace has unpushed commits")
	ErrManagedGit          = errors.New("managed workspace Git operation failed")
	ErrManagedRecord       = errors.New("managed workspace ownership record is invalid")
	managedBranchCharacter = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

type GitInvocation struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
}

type GitOutput struct {
	Stdout []byte
}

type GitRunner interface {
	Run(context.Context, GitInvocation) (GitOutput, error)
}

type OSGitRunner struct{}

func (OSGitRunner) Run(ctx context.Context, invocation GitInvocation) (GitOutput, error) {
	if err := ctx.Err(); err != nil {
		return GitOutput{}, err
	}
	if !cleanAbsolutePath(invocation.Executable) ||
		(invocation.WorkingDirectory != "" && !cleanAbsolutePath(invocation.WorkingDirectory)) {
		return GitOutput{}, ErrManagedRequest
	}
	for _, argument := range invocation.Arguments {
		if argument == "" || strings.ContainsRune(argument, 0) {
			return GitOutput{}, ErrManagedRequest
		}
	}
	output := &managedBoundedOutput{remaining: maximumGitOutputBytes}
	command := execCommandContext(ctx, invocation.Executable, invocation.Arguments...)
	command.Dir = invocation.WorkingDirectory
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return GitOutput{}, ctx.Err()
		}
		return GitOutput{}, ErrManagedGit
	}
	if output.exceeded {
		return GitOutput{}, ErrManagedGit
	}
	return GitOutput{Stdout: append([]byte(nil), output.Bytes()...)}, nil
}

// Kept as a variable solely so tests can exercise exact argv without a shell.
var execCommandContext = exec.CommandContext

type managedBoundedOutput struct {
	bytes.Buffer
	remaining int
	exceeded  bool
}

func (output *managedBoundedOutput) Write(contents []byte) (int, error) {
	original := len(contents)
	if output.remaining <= 0 {
		output.exceeded = true
		return original, nil
	}
	if len(contents) > output.remaining {
		contents = contents[:output.remaining]
		output.exceeded = true
	}
	output.remaining -= len(contents)
	_, _ = output.Buffer.Write(contents)
	return original, nil
}

type ManagedRepository struct {
	ID          string
	Root        string
	ManagedRoot string
	DefaultBase string
}

type ManagedConfig struct {
	GitExecutable string
	OwnershipRoot string
	Repositories  []ManagedRepository
	Runner        GitRunner
	Now           func() time.Time
}

type ManagedManager struct {
	gitExecutable string
	ownershipRoot string
	repositories  map[string]ManagedRepository
	runner        GitRunner
	now           func() time.Time
}

// OwnershipRoot is the directory holding managed-worktree ownership records.
func (manager *ManagedManager) OwnershipRoot() string {
	return manager.ownershipRoot
}

type CreateManagedRequest struct {
	RepositoryID string
	Branch       string
	StartPoint   string
}

type ArchiveManagedRequest struct {
	RepositoryID string
	WorktreePath string
}

type AdoptManagedRequest struct {
	RepositoryID string
	WorktreePath string
}

type ManagedResult struct {
	RepositoryID          string
	WorktreePath          string
	Branch                string
	HeadRevision          string
	AdministrativeGitPath string
	State                 string
	CreatedAt             time.Time
	ArchivedAt            *time.Time
}

type managedRecord struct {
	SchemaVersion        int        `json:"schemaVersion"`
	RepositoryID         string     `json:"repositoryId"`
	RepositoryRoot       string     `json:"repositoryRoot"`
	WorktreePath         string     `json:"worktreePath"`
	Branch               string     `json:"branch"`
	StartPoint           string     `json:"startPoint"`
	StartRevision        string     `json:"startRevision"`
	AdministrativeGitDir string     `json:"administrativeGitDir,omitempty"`
	HeadRevision         string     `json:"headRevision,omitempty"`
	State                string     `json:"state"`
	CreatedAt            time.Time  `json:"createdAt"`
	ArchivedAt           *time.Time `json:"archivedAt,omitempty"`
}

func NewManagedManager(config ManagedConfig) (*ManagedManager, error) {
	if !cleanAbsolutePath(config.GitExecutable) || !cleanAbsolutePath(config.OwnershipRoot) ||
		len(config.Repositories) == 0 || len(config.Repositories) > 64 {
		return nil, ErrManagedConfig
	}
	if config.Runner == nil {
		config.Runner = OSGitRunner{}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	repositories := make(map[string]ManagedRepository, len(config.Repositories))
	for _, repository := range config.Repositories {
		if !idPattern.MatchString(repository.ID) || !cleanAbsolutePath(repository.Root) ||
			!cleanAbsolutePath(repository.ManagedRoot) || repository.ManagedRoot == repository.Root ||
			repository.DefaultBase == "" || strings.ContainsRune(repository.DefaultBase, 0) {
			return nil, ErrManagedConfig
		}
		if _, duplicate := repositories[repository.ID]; duplicate {
			return nil, ErrManagedConfig
		}
		repositories[repository.ID] = repository
	}
	return &ManagedManager{
		gitExecutable: config.GitExecutable, ownershipRoot: config.OwnershipRoot,
		repositories: repositories, runner: config.Runner, now: config.Now,
	}, nil
}

func (manager *ManagedManager) Create(
	ctx context.Context,
	request CreateManagedRequest,
) (ManagedResult, error) {
	repository, found := manager.repositories[request.RepositoryID]
	if !found || request.Branch == "" || len(request.Branch) > 256 || strings.ContainsRune(request.Branch, 0) {
		return ManagedResult{}, ErrManagedRequest
	}
	if _, err := manager.git(ctx, repository.Root, "check-ref-format", "--branch", request.Branch); err != nil {
		return ManagedResult{}, ErrManagedRequest
	}
	startPoint := request.StartPoint
	if startPoint == "" {
		startPoint = repository.DefaultBase
	}
	startOutput, err := manager.git(ctx, repository.Root, "rev-parse", "--verify", startPoint+"^{commit}")
	if err != nil {
		return ManagedResult{}, ErrManagedRequest
	}
	startRevision, valid := canonicalGitObjectID(startOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedGit
	}
	worktreePath := managedWorktreePath(repository.ManagedRoot, request.Branch)
	if !pathInside(repository.ManagedRoot, worktreePath) {
		return ManagedResult{}, ErrManagedRequest
	}
	if _, err := os.Lstat(worktreePath); !errors.Is(err, os.ErrNotExist) {
		return ManagedResult{}, ErrManagedExists
	}
	if err := ensureManagedRoot(repository.ManagedRoot); err != nil {
		return ManagedResult{}, ErrManagedConfig
	}
	record := managedRecord{
		SchemaVersion: 1, RepositoryID: repository.ID, RepositoryRoot: repository.Root,
		WorktreePath: worktreePath, Branch: request.Branch, StartPoint: startPoint,
		StartRevision: startRevision, State: "creating", CreatedAt: manager.now().UTC(),
	}
	recordPath := manager.recordPath(worktreePath)
	if err := writeManagedRecord(recordPath, record, false); err != nil {
		return ManagedResult{}, ErrManagedRecord
	}
	if _, err := manager.git(
		ctx, repository.Root, "worktree", "add", "-b", request.Branch, worktreePath, startRevision,
	); err != nil {
		record.State = "failed"
		_ = writeManagedRecord(recordPath, record, true)
		return ManagedResult{}, err
	}
	administrativeOutput, err := manager.git(ctx, worktreePath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return ManagedResult{}, ErrManagedRecord
	}
	administrativePath, valid := canonicalAbsoluteLine(administrativeOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedRecord
	}
	headOutput, err := manager.git(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		return ManagedResult{}, ErrManagedRecord
	}
	headRevision, valid := canonicalGitObjectID(headOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedRecord
	}
	record.AdministrativeGitDir = administrativePath
	record.HeadRevision = headRevision
	record.State = "ready"
	if err := writeManagedRecord(recordPath, record, true); err != nil {
		return ManagedResult{}, ErrManagedRecord
	}
	return managedResult(record), nil
}

func (manager *ManagedManager) Archive(
	ctx context.Context,
	request ArchiveManagedRequest,
) (ManagedResult, error) {
	repository, found := manager.repositories[request.RepositoryID]
	if !found || !cleanAbsolutePath(request.WorktreePath) ||
		!pathInside(repository.ManagedRoot, request.WorktreePath) {
		return ManagedResult{}, ErrManagedRequest
	}
	recordPath := manager.recordPath(request.WorktreePath)
	record, err := readManagedRecord(recordPath)
	if err != nil || record.State != "ready" || record.RepositoryID != repository.ID ||
		record.RepositoryRoot != repository.Root || record.WorktreePath != request.WorktreePath {
		return ManagedResult{}, ErrManagedForeign
	}
	administrativeOutput, err := manager.git(ctx, request.WorktreePath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return ManagedResult{}, ErrManagedForeign
	}
	administrativePath, valid := canonicalAbsoluteLine(administrativeOutput.Stdout)
	if !valid || administrativePath != record.AdministrativeGitDir {
		return ManagedResult{}, ErrManagedForeign
	}
	statusOutput, err := manager.git(ctx, request.WorktreePath, "status", "--porcelain=v2", "--branch", "-z")
	if err != nil {
		return ManagedResult{}, ErrManagedGit
	}
	dirty, ahead, hasUpstream, valid := parseManagedStatus(statusOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedGit
	}
	if dirty {
		return ManagedResult{}, ErrManagedDirty
	}
	headOutput, err := manager.git(ctx, request.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return ManagedResult{}, ErrManagedGit
	}
	headRevision, valid := canonicalGitObjectID(headOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedGit
	}
	if ahead > 0 || !hasUpstream && headRevision != record.StartRevision {
		return ManagedResult{}, ErrManagedUnpushed
	}
	if _, err := manager.git(ctx, repository.Root, "worktree", "remove", request.WorktreePath); err != nil {
		return ManagedResult{}, err
	}
	archivedAt := manager.now().UTC()
	record.State = "archived"
	record.HeadRevision = headRevision
	record.ArchivedAt = &archivedAt
	if err := writeManagedRecord(recordPath, record, true); err != nil {
		return ManagedResult{}, ErrManagedRecord
	}
	return managedResult(record), nil
}

func (manager *ManagedManager) Adopt(
	ctx context.Context,
	request AdoptManagedRequest,
) (ManagedResult, error) {
	repository, found := manager.repositories[request.RepositoryID]
	if !found || !cleanAbsolutePath(request.WorktreePath) ||
		!pathInside(repository.ManagedRoot, request.WorktreePath) ||
		filepath.Dir(request.WorktreePath) != repository.ManagedRoot {
		return ManagedResult{}, ErrManagedRequest
	}
	worktreeInfo, err := os.Lstat(request.WorktreePath)
	if err != nil || !worktreeInfo.IsDir() || worktreeInfo.Mode()&os.ModeSymlink != 0 {
		return ManagedResult{}, ErrManagedRequest
	}
	recordPath := manager.recordPath(request.WorktreePath)
	if existing, err := readManagedRecord(recordPath); err == nil {
		if existing.State == "ready" && existing.RepositoryID == repository.ID &&
			existing.RepositoryRoot == repository.Root && existing.WorktreePath == request.WorktreePath {
			return managedResult(existing), nil
		}
		return ManagedResult{}, ErrManagedExists
	} else if _, statErr := os.Lstat(recordPath); !errors.Is(statErr, os.ErrNotExist) {
		return ManagedResult{}, ErrManagedRecord
	}

	repositoryCommonOutput, err := manager.git(
		ctx, repository.Root, "rev-parse", "--path-format=absolute", "--git-common-dir",
	)
	if err != nil {
		return ManagedResult{}, ErrManagedForeign
	}
	repositoryCommon, valid := canonicalAbsoluteLine(repositoryCommonOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedForeign
	}
	worktreeCommonOutput, err := manager.git(
		ctx, request.WorktreePath, "rev-parse", "--path-format=absolute", "--git-common-dir",
	)
	if err != nil {
		return ManagedResult{}, ErrManagedForeign
	}
	worktreeCommon, valid := canonicalAbsoluteLine(worktreeCommonOutput.Stdout)
	if !valid || worktreeCommon != repositoryCommon {
		return ManagedResult{}, ErrManagedForeign
	}
	administrativeOutput, err := manager.git(ctx, request.WorktreePath, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return ManagedResult{}, ErrManagedForeign
	}
	administrativePath, valid := canonicalAbsoluteLine(administrativeOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedForeign
	}
	branchOutput, err := manager.git(ctx, request.WorktreePath, "branch", "--show-current")
	if err != nil {
		return ManagedResult{}, ErrManagedForeign
	}
	branch, valid := canonicalManagedLine(branchOutput.Stdout, 256)
	if !valid {
		return ManagedResult{}, ErrManagedForeign
	}
	statusOutput, err := manager.git(ctx, request.WorktreePath, "status", "--porcelain=v2", "--branch", "-z")
	if err != nil {
		return ManagedResult{}, ErrManagedGit
	}
	dirty, ahead, hasUpstream, valid := parseManagedStatus(statusOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedGit
	}
	if dirty {
		return ManagedResult{}, ErrManagedDirty
	}
	if ahead > 0 || !hasUpstream {
		return ManagedResult{}, ErrManagedUnpushed
	}
	upstreamOutput, err := manager.git(
		ctx, request.WorktreePath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}",
	)
	if err != nil {
		return ManagedResult{}, ErrManagedUnpushed
	}
	upstream, valid := canonicalManagedLine(upstreamOutput.Stdout, 256)
	if !valid {
		return ManagedResult{}, ErrManagedUnpushed
	}
	headOutput, err := manager.git(ctx, request.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return ManagedResult{}, ErrManagedGit
	}
	headRevision, valid := canonicalGitObjectID(headOutput.Stdout)
	if !valid {
		return ManagedResult{}, ErrManagedGit
	}
	record := managedRecord{
		SchemaVersion: 1, RepositoryID: repository.ID, RepositoryRoot: repository.Root,
		WorktreePath: request.WorktreePath, Branch: branch, StartPoint: upstream,
		StartRevision: headRevision, AdministrativeGitDir: administrativePath,
		HeadRevision: headRevision, State: "ready", CreatedAt: manager.now().UTC(),
	}
	if err := writeManagedRecord(recordPath, record, false); err != nil {
		return ManagedResult{}, ErrManagedRecord
	}
	return managedResult(record), nil
}

func (manager *ManagedManager) Owns(repositoryID string, worktreePath string) bool {
	repository, found := manager.repositories[repositoryID]
	if !found || !cleanAbsolutePath(worktreePath) || !pathInside(repository.ManagedRoot, worktreePath) {
		return false
	}
	record, err := readManagedRecord(manager.recordPath(worktreePath))
	return err == nil && record.State == "ready" && record.RepositoryID == repositoryID &&
		record.RepositoryRoot == repository.Root && record.WorktreePath == worktreePath &&
		cleanAbsolutePath(record.AdministrativeGitDir)
}

func (manager *ManagedManager) git(
	ctx context.Context,
	directory string,
	arguments ...string,
) (GitOutput, error) {
	argv := []string{"-C", directory}
	argv = append(argv, arguments...)
	return manager.runner.Run(ctx, GitInvocation{Executable: manager.gitExecutable, Arguments: argv})
}

func (manager *ManagedManager) recordPath(worktreePath string) string {
	digest := sha256.Sum256([]byte("managed-workspace-v1\x00" + worktreePath))
	return filepath.Join(manager.ownershipRoot, hex.EncodeToString(digest[:16])+".json")
}

func managedWorktreePath(root string, branch string) string {
	slug := strings.Trim(managedBranchCharacter.ReplaceAllString(branch, "-"), "-._")
	if slug == "" {
		slug = "workspace"
	}
	if len(slug) > 64 {
		slug = slug[:64]
	}
	digest := sha256.Sum256([]byte(branch))
	return filepath.Join(root, slug+"-"+hex.EncodeToString(digest[:4]))
}

func ensureManagedRoot(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrManagedConfig
	}
	return os.Chmod(path, 0o700)
}

func writeManagedRecord(path string, record managedRecord, replace bool) error {
	directory := filepath.Dir(path)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	if !replace {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return ErrManagedExists
		}
	} else {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm() != 0o600 {
			return ErrManagedRecord
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return ErrManagedRecord
		}
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".workspace-record-*")
	if err != nil {
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if replace {
		if err := os.Rename(temporaryPath, path); err != nil {
			return err
		}
	} else {
		if err := os.Link(temporaryPath, path); err != nil {
			return ErrManagedExists
		}
		if err := os.Remove(temporaryPath); err != nil {
			return err
		}
	}
	cleanup = false
	return syncDirectory(directory)
}

func readManagedRecord(path string) (managedRecord, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64*1024 {
		return managedRecord{}, ErrManagedRecord
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return managedRecord{}, ErrManagedRecord
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var record managedRecord
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		record.SchemaVersion != 1 || !idPattern.MatchString(record.RepositoryID) ||
		!cleanAbsolutePath(record.RepositoryRoot) || !cleanAbsolutePath(record.WorktreePath) ||
		!validManagedText(record.Branch, 256) || !validManagedText(record.StartPoint, 256) ||
		!validGitObjectID(record.StartRevision) || record.CreatedAt.IsZero() ||
		(record.State != "creating" && record.State != "ready" && record.State != "failed" && record.State != "archived") ||
		!validManagedRecordState(record) {
		return managedRecord{}, ErrManagedRecord
	}
	return record, nil
}

func validManagedRecordState(record managedRecord) bool {
	switch record.State {
	case "creating", "failed":
		return record.AdministrativeGitDir == "" && record.HeadRevision == "" && record.ArchivedAt == nil
	case "ready":
		return cleanAbsolutePath(record.AdministrativeGitDir) && validGitObjectID(record.HeadRevision) &&
			record.ArchivedAt == nil
	case "archived":
		return cleanAbsolutePath(record.AdministrativeGitDir) && validGitObjectID(record.HeadRevision) &&
			record.ArchivedAt != nil && !record.ArchivedAt.IsZero()
	default:
		return false
	}
}

func validManagedText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character == 0 || character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validGitObjectID(value string) bool {
	canonical, valid := canonicalGitObjectID([]byte(value))
	return valid && canonical == value
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrManagedRecord
	}
	return os.Chmod(path, 0o700)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func canonicalAbsoluteLine(output []byte) (string, bool) {
	value := strings.TrimSpace(string(output))
	return value, cleanAbsolutePath(value) && !strings.ContainsAny(value, "\r\n")
}

func canonicalManagedLine(output []byte, maximum int) (string, bool) {
	value := strings.TrimSuffix(string(output), "\n")
	value = strings.TrimSuffix(value, "\r")
	return value, validManagedText(value, maximum) && !strings.ContainsAny(value, "\r\n")
}

func canonicalGitObjectID(output []byte) (string, bool) {
	value := strings.TrimSpace(string(output))
	if len(value) != 40 && len(value) != 64 || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return "", false
		}
	}
	return value, true
}

// parseManagedStatus reads porcelain v2 branch headers. hasUpstream is true
// only when Git both names an upstream and could compare against it
// (branch.ab present): a configured upstream whose ref is gone, such as a
// remote branch deleted after a merge and pruned locally, reports no ahead
// count at all, and the caller must then fall back to the start-revision
// comparison rather than treat the branch as pushed.
func parseManagedStatus(output []byte) (dirty bool, ahead int, hasUpstream bool, valid bool) {
	valid = true
	upstreamNamed, comparable := false, false
	for _, field := range bytes.Split(output, []byte{0}) {
		if len(field) == 0 {
			continue
		}
		line := string(field)
		if !strings.HasPrefix(line, "# ") {
			dirty = true
			continue
		}
		if strings.HasPrefix(line, "# branch.upstream ") {
			upstreamNamed = strings.TrimSpace(strings.TrimPrefix(line, "# branch.upstream ")) != ""
		}
		if strings.HasPrefix(line, "# branch.ab ") {
			comparable = true
			parts := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(parts) != 2 || !strings.HasPrefix(parts[0], "+") || !strings.HasPrefix(parts[1], "-") {
				return false, 0, false, false
			}
			parsed, err := strconv.Atoi(strings.TrimPrefix(parts[0], "+"))
			if err != nil || parsed < 0 {
				return false, 0, false, false
			}
			ahead = parsed
		}
	}
	hasUpstream = upstreamNamed && comparable
	return dirty, ahead, hasUpstream, valid
}

func pathInside(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func managedResult(record managedRecord) ManagedResult {
	return ManagedResult{
		RepositoryID: record.RepositoryID, WorktreePath: record.WorktreePath,
		Branch: record.Branch, HeadRevision: record.HeadRevision,
		AdministrativeGitPath: record.AdministrativeGitDir, State: record.State,
		CreatedAt: record.CreatedAt, ArchivedAt: record.ArchivedAt,
	}
}

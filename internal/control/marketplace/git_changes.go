package marketplacecontrol

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
)

const maximumUntrackedFileBytes = 4 * 1024 * 1024
const maximumChangedFiles = 10000

type LineChanges struct {
	Additions int64
	Deletions int64
	Files     int
}

type ServiceLineChanges struct {
	ServiceID   string
	Committed   LineChanges
	Uncommitted LineChanges
}

type WorktreeChanges struct {
	BaseRevision       string
	Committed          LineChanges
	Uncommitted        LineChanges
	SharedCommitted    LineChanges
	SharedUncommitted  LineChanges
	Services           []ServiceLineChanges
	HasTrackedChanges  bool
	HasUntrackedFiles  bool
	HasUnpushedCommits bool
}

type GitChangeReader struct {
	runner        marketplaceadapter.CommandRunner
	gitExecutable string
	baseReference string
	sourceRoots   []marketplaceadapter.RuntimeServiceSource
}

func NewGitChangeReader(
	runner marketplaceadapter.CommandRunner,
	gitExecutable string,
	baseReference string,
) (GitChangeReader, error) {
	if runner == nil || gitExecutable == "" || baseReference == "" {
		return GitChangeReader{}, errors.New("Marketplace Git change reader is unavailable")
	}
	return GitChangeReader{
		runner: runner, gitExecutable: gitExecutable, baseReference: baseReference,
		sourceRoots: marketplaceadapter.RuntimeServiceSources(),
	}, nil
}

func (reader GitChangeReader) Read(ctx context.Context, worktreeRoot string) (WorktreeChanges, error) {
	if !filepath.IsAbs(worktreeRoot) {
		return WorktreeChanges{}, errors.New("Marketplace worktree path is invalid")
	}
	baseOutput, err := reader.git(ctx, worktreeRoot, "merge-base", reader.baseReference, "HEAD")
	if err != nil {
		return WorktreeChanges{}, errors.New("Marketplace merge base is unavailable")
	}
	baseRevision, valid := parseGitObjectID(baseOutput)
	if !valid {
		return WorktreeChanges{}, errors.New("Marketplace merge base is invalid")
	}
	committedOutput, err := reader.git(
		ctx, worktreeRoot, "diff", "--no-renames", "--numstat", "-z", baseRevision, "HEAD", "--",
	)
	if err != nil {
		return WorktreeChanges{}, errors.New("committed Marketplace changes are unavailable")
	}
	uncommittedOutput, err := reader.git(
		ctx, worktreeRoot, "diff", "--no-renames", "--numstat", "-z", "HEAD", "--",
	)
	if err != nil {
		return WorktreeChanges{}, errors.New("uncommitted Marketplace changes are unavailable")
	}
	untrackedOutput, err := reader.git(ctx, worktreeRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return WorktreeChanges{}, errors.New("untracked Marketplace files are unavailable")
	}

	committedFiles, err := parseNumstat(committedOutput)
	if err != nil {
		return WorktreeChanges{}, errors.New("committed Marketplace changes are invalid")
	}
	uncommittedFiles, err := parseNumstat(uncommittedOutput)
	if err != nil {
		return WorktreeChanges{}, errors.New("uncommitted Marketplace changes are invalid")
	}
	untrackedPaths, err := parseNULPaths(untrackedOutput)
	if err != nil {
		return WorktreeChanges{}, errors.New("untracked Marketplace files are invalid")
	}
	for _, path := range untrackedPaths {
		uncommittedFiles = append(uncommittedFiles, changedFile{
			Path: path, Changes: countUntrackedFile(worktreeRoot, path),
		})
	}

	result := WorktreeChanges{BaseRevision: baseRevision, HasUntrackedFiles: len(untrackedPaths) > 0}
	result.Committed, result.SharedCommitted, result.Services = reader.aggregate(committedFiles, result.Services, true)
	result.Uncommitted, result.SharedUncommitted, result.Services = reader.aggregate(uncommittedFiles, result.Services, false)
	result.HasTrackedChanges = len(uncommittedFiles)-len(untrackedPaths) > 0
	result.HasUnpushedCommits = reader.hasUnpushedCommits(ctx, worktreeRoot, result.Committed.Files > 0)
	sort.Slice(result.Services, func(left, right int) bool { return result.Services[left].ServiceID < result.Services[right].ServiceID })
	return result, nil
}

type changedFile struct {
	Path    string
	Changes LineChanges
}

func (reader GitChangeReader) aggregate(
	files []changedFile,
	services []ServiceLineChanges,
	committed bool,
) (LineChanges, LineChanges, []ServiceLineChanges) {
	byService := make(map[string]int, len(services))
	for index := range services {
		byService[services[index].ServiceID] = index
	}
	var total LineChanges
	var shared LineChanges
	for _, file := range files {
		total.add(file.Changes)
		serviceID := reader.serviceID(file.Path)
		if serviceID == "" {
			shared.add(file.Changes)
			continue
		}
		serviceIndex, found := byService[serviceID]
		if !found {
			services = append(services, ServiceLineChanges{ServiceID: serviceID})
			serviceIndex = len(services) - 1
			byService[serviceID] = serviceIndex
		}
		if committed {
			services[serviceIndex].Committed.add(file.Changes)
		} else {
			services[serviceIndex].Uncommitted.add(file.Changes)
		}
	}
	return total, shared, services
}

func (changes *LineChanges) add(other LineChanges) {
	changes.Additions += other.Additions
	changes.Deletions += other.Deletions
	changes.Files += other.Files
}

func (reader GitChangeReader) serviceID(path string) string {
	for _, source := range reader.sourceRoots {
		if path == source.Root || strings.HasPrefix(path, source.Root+"/") {
			return source.ServiceID
		}
	}
	return ""
}

func (reader GitChangeReader) hasUnpushedCommits(ctx context.Context, root string, fallback bool) bool {
	output, err := reader.git(ctx, root, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return fallback
	}
	count, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	return err == nil && count > 0
}

func (reader GitChangeReader) git(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	output, err := reader.runner.Run(ctx, marketplaceadapter.Invocation{
		Executable: reader.gitExecutable,
		Arguments:  append([]string{"-C", root}, arguments...),
	})
	return output.Stdout, err
}

func parseGitObjectID(contents []byte) (string, bool) {
	value := strings.TrimSpace(string(contents))
	if (len(value) != 40 && len(value) != 64) || strings.ContainsAny(value, " \t\r\n") {
		return "", false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return "", false
		}
	}
	return value, true
}

func parseNumstat(contents []byte) ([]changedFile, error) {
	if len(contents) == 0 {
		return []changedFile{}, nil
	}
	if contents[len(contents)-1] != 0 {
		return nil, errors.New("numstat output is not NUL terminated")
	}
	records := bytes.Split(contents[:len(contents)-1], []byte{0})
	if len(records) > maximumChangedFiles {
		return nil, errors.New("numstat output has too many files")
	}
	files := make([]changedFile, 0, len(records))
	for _, record := range records {
		fields := bytes.Split(record, []byte{'\t'})
		if len(fields) != 3 {
			return nil, errors.New("numstat record is malformed")
		}
		path, valid := validRelativePath(string(fields[2]))
		if !valid {
			return nil, errors.New("numstat path is invalid")
		}
		additions, additionsValid := parseNumstatCount(fields[0])
		deletions, deletionsValid := parseNumstatCount(fields[1])
		if !additionsValid || !deletionsValid {
			return nil, errors.New("numstat count is invalid")
		}
		files = append(files, changedFile{Path: path, Changes: LineChanges{
			Additions: additions, Deletions: deletions, Files: 1,
		}})
	}
	return files, nil
}

func parseNumstatCount(contents []byte) (int64, bool) {
	if bytes.Equal(contents, []byte("-")) {
		return 0, true
	}
	value, err := strconv.ParseInt(string(contents), 10, 64)
	return value, err == nil && value >= 0
}

func parseNULPaths(contents []byte) ([]string, error) {
	if len(contents) == 0 {
		return []string{}, nil
	}
	if contents[len(contents)-1] != 0 {
		return nil, errors.New("path output is not NUL terminated")
	}
	records := bytes.Split(contents[:len(contents)-1], []byte{0})
	if len(records) > maximumChangedFiles {
		return nil, errors.New("path output has too many files")
	}
	paths := make([]string, 0, len(records))
	for _, record := range records {
		path, valid := validRelativePath(string(record))
		if !valid {
			return nil, errors.New("path output is invalid")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func validRelativePath(path string) (string, bool) {
	if path == "" || filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", false
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(clean), true
}

func countUntrackedFile(root, relativePath string) LineChanges {
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumUntrackedFileBytes {
		return LineChanges{Files: 1}
	}
	contents, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(contents, 0) >= 0 {
		return LineChanges{Files: 1}
	}
	lineCount := int64(bytes.Count(contents, []byte{'\n'}))
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		lineCount++
	}
	return LineChanges{Additions: lineCount, Files: 1}
}

package cleanup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const maximumInventoryEntries = 10_000

var fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var (
	ErrInvalidScope      = errors.New("cleanup scope is invalid")
	ErrCandidateChanged  = errors.New("cleanup candidate changed after planning")
	ErrProtectedResource = errors.New("cleanup resource is protected")
)

type Scope struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type Candidate struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	ProfileKey  string `json:"profileKey"`
	WorktreeID  string `json:"worktreeId"`
	Fingerprint string `json:"fingerprint"`
	Bytes       int64  `json:"bytes"`
	Path        string `json:"path"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	ModifiedNS  int64  `json:"modifiedNs"`
}

type Protection struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	ProfileKey string `json:"profileKey,omitempty"`
	WorktreeID string `json:"worktreeId,omitempty"`
}

type Inventory struct {
	Candidates []Candidate  `json:"candidates"`
	Protected  []Protection `json:"protected"`
}

type Plan struct {
	SchemaVersion int          `json:"schemaVersion"`
	ID            string       `json:"id"`
	Revision      int64        `json:"revision"`
	Scope         Scope        `json:"scope"`
	Candidates    []Candidate  `json:"candidates"`
	Protected     []Protection `json:"protected"`
	CreatedAt     time.Time    `json:"createdAt"`
	ExpiresAt     time.Time    `json:"expiresAt"`
}

type Removal struct {
	CandidateID string `json:"candidateId"`
	Removed     bool   `json:"removed"`
	Reason      string `json:"reason,omitempty"`
}

type Result struct {
	SchemaVersion int       `json:"schemaVersion"`
	PlanID        string    `json:"planId"`
	PlanRevision  int64     `json:"planRevision"`
	Removals      []Removal `json:"removals"`
	ClaimedAt     time.Time `json:"claimedAt"`
	Attempts      int       `json:"attempts"`
	CompletedAt   time.Time `json:"completedAt"`
}

type PrivatePreparationPlanner struct {
	RuntimeRoot         string
	CurrentFingerprints map[string]string
}

func (planner PrivatePreparationPlanner) Inventory(ctx context.Context, scope Scope) (Inventory, error) {
	if !validScope(scope) || !cleanAbsoluteDirectory(planner.RuntimeRoot) {
		return Inventory{}, ErrInvalidScope
	}
	result := Inventory{Candidates: []Candidate{}, Protected: []Protection{}}
	repositoriesRoot := filepath.Join(planner.RuntimeRoot, "repositories")
	entries, err := readOptionalDirectory(repositoriesRoot)
	if err != nil {
		return Inventory{}, err
	}
	count := 0
	for _, profileEntry := range entries {
		if err := ctx.Err(); err != nil {
			return Inventory{}, err
		}
		profileKey := profileEntry.Name()
		if scope.Kind == "repository" && scope.ID != profileKey {
			continue
		}
		profileRoot := filepath.Join(repositoriesRoot, profileKey)
		if !directoryEntrySafe(profileEntry, profileRoot) {
			result.Protected = append(result.Protected, protection(profileRoot, "unverified", profileKey, ""))
			continue
		}
		worktrees, err := os.ReadDir(profileRoot)
		if err != nil {
			return Inventory{}, err
		}
		for _, worktreeEntry := range worktrees {
			count++
			if count > maximumInventoryEntries {
				return Inventory{}, errors.New("cleanup inventory exceeds its entry limit")
			}
			worktreeID := worktreeEntry.Name()
			if scope.Kind == "worktree" && scope.ID != worktreeID {
				continue
			}
			worktreeRoot := filepath.Join(profileRoot, worktreeID)
			if !directoryEntrySafe(worktreeEntry, worktreeRoot) {
				result.Protected = append(result.Protected, protection(worktreeRoot, "unverified", profileKey, worktreeID))
				continue
			}
			preparationRoot := filepath.Join(worktreeRoot, "preparation")
			fingerprints, err := readOptionalDirectory(preparationRoot)
			if err != nil {
				return Inventory{}, err
			}
			for _, fingerprintEntry := range fingerprints {
				count++
				if count > maximumInventoryEntries {
					return Inventory{}, errors.New("cleanup inventory exceeds its entry limit")
				}
				fingerprint := fingerprintEntry.Name()
				candidatePath := filepath.Join(preparationRoot, fingerprint)
				if !fingerprintPattern.MatchString(fingerprint) || !directoryEntrySafe(fingerprintEntry, candidatePath) {
					result.Protected = append(result.Protected, protection(candidatePath, "unverified", profileKey, worktreeID))
					continue
				}
				if planner.CurrentFingerprints[worktreeID] == fingerprint {
					result.Protected = append(result.Protected, protection(candidatePath, "current", profileKey, worktreeID))
					continue
				}
				candidate, err := inspectCandidate(candidatePath, profileKey, worktreeID, fingerprint)
				if err != nil {
					result.Protected = append(result.Protected, protection(candidatePath, "foreign-or-modified", profileKey, worktreeID))
					continue
				}
				result.Candidates = append(result.Candidates, candidate)
			}
		}
	}
	sort.Slice(result.Candidates, func(left, right int) bool { return result.Candidates[left].ID < result.Candidates[right].ID })
	sort.Slice(result.Protected, func(left, right int) bool { return result.Protected[left].Path < result.Protected[right].Path })
	return result, nil
}

func (planner PrivatePreparationPlanner) Remove(ctx context.Context, candidate Candidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !cleanAbsoluteDirectory(planner.RuntimeRoot) ||
		!pathContained(filepath.Join(planner.RuntimeRoot, "repositories"), candidate.Path) ||
		!realDirectoryChain(planner.RuntimeRoot, candidate.Path) {
		return ErrProtectedResource
	}
	inspected, err := inspectCandidate(candidate.Path, candidate.ProfileKey, candidate.WorktreeID, candidate.Fingerprint)
	if err != nil || inspected.ID != candidate.ID || inspected.Device != candidate.Device ||
		inspected.Inode != candidate.Inode || inspected.ModifiedNS != candidate.ModifiedNS {
		return ErrCandidateChanged
	}
	if planner.CurrentFingerprints[candidate.WorktreeID] == candidate.Fingerprint {
		return ErrProtectedResource
	}
	steps, err := os.ReadDir(candidate.Path)
	if err != nil {
		return ErrCandidateChanged
	}
	for _, step := range steps {
		stepPath := filepath.Join(candidate.Path, step.Name())
		files, err := os.ReadDir(stepPath)
		if err != nil {
			return ErrCandidateChanged
		}
		// Logs go first and the ownership marker last, so a removal that is
		// interrupted part-way leaves a step that is still positively owned
		// and re-plannable instead of an unverifiable leftover.
		sort.SliceStable(files, func(left, right int) bool {
			return files[left].Name() != "ownership.json" && files[right].Name() == "ownership.json"
		})
		for _, file := range files {
			if file.Name() != "ownership.json" && file.Name() != "stdout.log" && file.Name() != "stderr.log" {
				return ErrCandidateChanged
			}
			path := filepath.Join(stepPath, file.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return ErrCandidateChanged
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		if err := os.Remove(stepPath); err != nil {
			return err
		}
	}
	if err := os.Remove(candidate.Path); err != nil {
		return err
	}
	return nil
}

func inspectCandidate(path, profileKey, worktreeID, fingerprint string) (Candidate, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Candidate{}, ErrProtectedResource
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Candidate{}, ErrProtectedResource
	}
	steps, err := os.ReadDir(path)
	if err != nil || len(steps) == 0 {
		return Candidate{}, ErrProtectedResource
	}
	var bytes int64
	for _, step := range steps {
		stepPath := filepath.Join(path, step.Name())
		if !directoryEntrySafe(step, stepPath) {
			return Candidate{}, ErrProtectedResource
		}
		files, err := os.ReadDir(stepPath)
		if err != nil {
			return Candidate{}, err
		}
		seenMarker := false
		for _, file := range files {
			if file.Name() != "ownership.json" && file.Name() != "stdout.log" && file.Name() != "stderr.log" {
				return Candidate{}, ErrProtectedResource
			}
			filePath := filepath.Join(stepPath, file.Name())
			fileInfo, err := os.Lstat(filePath)
			if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
				return Candidate{}, ErrProtectedResource
			}
			fileStat, ok := fileInfo.Sys().(*syscall.Stat_t)
			if !ok || fileStat.Nlink != 1 {
				return Candidate{}, ErrProtectedResource
			}
			bytes += fileInfo.Size()
			if file.Name() == "ownership.json" {
				if !validMarker(filePath, step.Name()) {
					return Candidate{}, ErrProtectedResource
				}
				seenMarker = true
			}
		}
		if !seenMarker {
			return Candidate{}, ErrProtectedResource
		}
	}
	identity := fmt.Sprintf("%s\x00%d\x00%d\x00%d", path, stat.Dev, stat.Ino, stat.Mtimespec.Nano())
	digest := sha256.Sum256([]byte(identity))
	return Candidate{
		ID: "cleanup_" + hex.EncodeToString(digest[:16]), Kind: "private-preparation",
		ProfileKey: profileKey, WorktreeID: worktreeID, Fingerprint: fingerprint,
		Bytes: bytes, Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, ModifiedNS: stat.Mtimespec.Nano(),
	}, nil
}

func validMarker(path, stepID string) bool {
	contents, err := os.ReadFile(path)
	if err != nil || len(contents) > 1024 {
		return false
	}
	var marker struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		StepID        string `json:"stepId"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil {
		return false
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return false
	}
	return marker.SchemaVersion == 1 && marker.Kind == "preparation-step" && marker.StepID == stepID
}

// readOptionalDirectory lists a fixed-name component of the private runtime
// tree. A missing component is empty; a component that is not a real
// directory (a symlink in particular) is refused, because every path derived
// from it would otherwise be reported, and later removed, as if it were
// inside the runtime root.
func readOptionalDirectory(path string) ([]fs.DirEntry, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return []fs.DirEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrProtectedResource
	}
	return os.ReadDir(path)
}

// realDirectoryChain proves that every component from root down to path is a
// real directory, never a symlink, so a lexically contained path is also
// physically contained.
func realDirectoryChain(root, path string) bool {
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
	return true
}

func directoryEntrySafe(entry fs.DirEntry, path string) bool {
	if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func cleanAbsoluteDirectory(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func pathContained(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validScope(scope Scope) bool {
	switch scope.Kind {
	case "global":
		return scope.ID == ""
	case "repository", "worktree":
		return scope.ID != "" && !strings.ContainsAny(scope.ID, "/\x00")
	default:
		return false
	}
}

func protection(path, reason, profileKey, worktreeID string) Protection {
	return Protection{Kind: "private-preparation", Path: path, Reason: reason, ProfileKey: profileKey, WorktreeID: worktreeID}
}

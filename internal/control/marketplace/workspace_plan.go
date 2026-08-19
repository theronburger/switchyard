package marketplacecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/control/workspace"
)

const (
	maximumWorkspaceManifestFiles = 4096
	maximumWorkspaceManifestBytes = 4 * 1024 * 1024
	marketplaceHydrationTimeout   = 30 * time.Minute
)

var ErrMarketplaceWorkspaceInvalid = errors.New("Marketplace workspace plan is invalid")

type WorkspaceRegistration struct {
	WorktreeID         string
	WorktreeRoot       string
	NodeExecutable     string
	NodeRequested      string
	NodeResolved       string
	YarnCJS            string
	RunRoot            string
	HomeDirectory      string
	TemporaryDirectory string
	ExecutablePath     string
	Ownership          workspace.Ownership
}

type WorkspacePlanBuilder struct {
	byWorktree map[string]WorkspaceRegistration
}

func NewWorkspacePlanBuilder(registrations []WorkspaceRegistration) (WorkspacePlanBuilder, error) {
	byWorktree := make(map[string]WorkspaceRegistration, len(registrations))
	for _, registration := range registrations {
		if !validWorkspaceRegistration(registration) {
			return WorkspacePlanBuilder{}, ErrMarketplaceWorkspaceInvalid
		}
		if _, duplicate := byWorktree[registration.WorktreeID]; duplicate {
			return WorkspacePlanBuilder{}, ErrMarketplaceWorkspaceInvalid
		}
		byWorktree[registration.WorktreeID] = registration
	}
	if len(byWorktree) == 0 {
		return WorkspacePlanBuilder{}, ErrMarketplaceWorkspaceInvalid
	}
	return WorkspacePlanBuilder{byWorktree: byWorktree}, nil
}

func (builder WorkspacePlanBuilder) Build(request workspace.PlanningRequest) (workspace.Plan, error) {
	registration, found := builder.byWorktree[request.WorktreeID]
	if !found || !registryIDPattern.MatchString(request.OperationID) {
		return workspace.Plan{}, ErrMarketplaceWorkspaceInvalid
	}
	fingerprint, err := marketplaceWorkspaceFingerprint(registration)
	if err != nil {
		return workspace.Plan{}, ErrMarketplaceWorkspaceInvalid
	}
	environmentVariables := []string{
		"HOME=" + registration.HomeDirectory,
		"PATH=" + registration.ExecutablePath,
		"TMPDIR=" + registration.TemporaryDirectory,
		"TURBO_CACHE_DIR=" + filepath.Join(registration.RunRoot, "caches", "turbo"),
		"YARN_NM_MODE=hardlinks-global",
	}
	sort.Strings(environmentVariables)
	return workspace.Plan{
		WorktreeID: request.WorktreeID, Adapter: marketplaceAdapterID,
		WorktreeRoot: registration.WorktreeRoot, Ownership: registration.Ownership,
		Fingerprint: fingerprint,
		Steps: []workspace.StepSpec{{
			ID: "hydrate-dependencies", Executable: registration.NodeExecutable,
			Arguments:   []string{registration.YarnCJS, "install", "--immutable"},
			Environment: environmentVariables, Directory: registration.WorktreeRoot,
			RunDirectory: filepath.Join(
				registration.RunRoot, "workspaces", request.WorktreeID, "hydration", fingerprint,
			),
			Timeout: marketplaceHydrationTimeout,
		}},
		Requirements: []workspace.Requirement{
			{ID: "node", Path: registration.NodeExecutable, Kind: workspace.RequirementExecutable},
			{ID: "yarn", Path: registration.YarnCJS, Kind: workspace.RequirementRegularFile},
			{ID: "node-modules", Path: filepath.Join(registration.WorktreeRoot, "node_modules"), Kind: workspace.RequirementDirectory},
			{ID: "install-state", Path: filepath.Join(registration.WorktreeRoot, ".yarn", "install-state.gz"), Kind: workspace.RequirementRegularFile},
		},
		Toolchains: []workspace.Toolchain{{
			ID: "node", RequestedVersion: registration.NodeRequested,
			ResolvedVersion: registration.NodeResolved, Executable: registration.NodeExecutable,
		}},
	}, nil
}

func validWorkspaceRegistration(registration WorkspaceRegistration) bool {
	if !registryIDPattern.MatchString(registration.WorktreeID) ||
		!cleanAbsolutePath(registration.WorktreeRoot) || !cleanAbsolutePath(registration.NodeExecutable) ||
		!cleanAbsolutePath(registration.YarnCJS) || !cleanAbsolutePath(registration.RunRoot) ||
		!cleanAbsolutePath(registration.HomeDirectory) || !cleanAbsolutePath(registration.TemporaryDirectory) ||
		!validExecutablePath(registration.ExecutablePath) || registration.NodeRequested == "" ||
		registration.NodeResolved == "" || (registration.Ownership != workspace.OwnershipAdopted &&
		registration.Ownership != workspace.OwnershipManaged) {
		return false
	}
	rootInfo, err := os.Lstat(registration.WorktreeRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 ||
		!pathWithin(registration.WorktreeRoot, registration.YarnCJS) {
		return false
	}
	return true
}

func marketplaceWorkspaceFingerprint(registration WorkspaceRegistration) (string, error) {
	type manifest struct {
		path string
		hash [sha256.Size]byte
	}
	manifests := make([]manifest, 0, 256)
	err := filepath.WalkDir(registration.WorktreeRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == registration.WorktreeRoot {
			return nil
		}
		relative, err := filepath.Rel(registration.WorktreeRoot, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrMarketplaceWorkspaceInvalid
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".turbo":
				return filepath.SkipDir
			case "cache":
				if filepath.Base(filepath.Dir(path)) == ".yarn" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.Name() != "package.json" &&
			relative != "yarn.lock" && relative != ".yarnrc.yml" && relative != ".nvmrc" {
			return nil
		}
		if len(manifests) >= maximumWorkspaceManifestFiles {
			return ErrMarketplaceWorkspaceInvalid
		}
		digest, err := hashBoundedRegularFile(path, maximumWorkspaceManifestBytes)
		if err != nil {
			return err
		}
		manifests = append(manifests, manifest{path: filepath.ToSlash(relative), hash: digest})
		return nil
	})
	if err != nil || len(manifests) < 4 {
		return "", ErrMarketplaceWorkspaceInvalid
	}
	sort.Slice(manifests, func(left, right int) bool { return manifests[left].path < manifests[right].path })
	digest := sha256.New()
	_, _ = io.WriteString(digest, "switchyard-marketplace-workspace-v1\x00")
	_, _ = io.WriteString(digest, registration.NodeRequested+"\x00"+registration.NodeResolved+"\x00")
	for _, item := range manifests {
		_, _ = io.WriteString(digest, item.path+"\x00")
		_, _ = digest.Write(item.hash[:])
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashBoundedRegularFile(path string, maximumBytes int64) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maximumBytes {
		return empty, ErrMarketplaceWorkspaceInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return empty, ErrMarketplaceWorkspaceInvalid
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	written, err := io.CopyN(digest, file, maximumBytes+1)
	if err != nil && !errors.Is(err, io.EOF) || written != info.Size() {
		return empty, ErrMarketplaceWorkspaceInvalid
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

// WorkspacePreparationRunner reuses the hardened finite-command host while
// keeping the workspace coordinator independent of Marketplace and Node.
type WorkspacePreparationRunner struct {
	Runner OSPreparationRunner
}

func (runner WorkspacePreparationRunner) Run(ctx context.Context, step workspace.StepSpec) error {
	return runner.Runner.Run(ctx, environment.PreparationSpec{
		ID: step.ID, Executable: step.Executable, Arguments: append([]string(nil), step.Arguments...),
		Environment: append([]string(nil), step.Environment...), Directory: step.Directory,
		RunDirectory: step.RunDirectory, Timeout: step.Timeout,
	})
}

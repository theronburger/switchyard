package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
)

const (
	maximumProfileFingerprintFiles = 4096
	maximumProfileFingerprintBytes = 4 * 1024 * 1024
)

type ProfileRegistration struct {
	WorktreeID    string
	WorktreeRoot  string
	ProfileKey    string
	ProfileDigest string
	RuntimeRoot   string
	Ownership     Ownership
	Preparation   configuration.Preparation
}

type ProfilePlanBuilder struct {
	registrations map[string]ProfileRegistration
	home          string
	temporary     string
}

func NewProfilePlanBuilder(registrations []ProfileRegistration) (ProfilePlanBuilder, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ProfilePlanBuilder{}, ErrInvalidPlan
	}
	builder := ProfilePlanBuilder{
		registrations: make(map[string]ProfileRegistration, len(registrations)),
		home:          filepath.Clean(home), temporary: filepath.Clean(os.TempDir()),
	}
	for _, registration := range registrations {
		if !validProfileRegistration(registration) {
			return ProfilePlanBuilder{}, ErrInvalidPlan
		}
		if _, duplicate := builder.registrations[registration.WorktreeID]; duplicate {
			return ProfilePlanBuilder{}, ErrInvalidPlan
		}
		builder.registrations[registration.WorktreeID] = registration
	}
	if len(builder.registrations) == 0 {
		return ProfilePlanBuilder{}, ErrInvalidPlan
	}
	return builder, nil
}

func (builder ProfilePlanBuilder) Build(request PlanningRequest) (Plan, error) {
	registration, found := builder.registrations[request.WorktreeID]
	if !found || request.OperationID == "" {
		return Plan{}, ErrInvalidPlan
	}
	fingerprint, err := profileFingerprint(registration)
	if err != nil {
		return Plan{}, ErrInvalidPlan
	}
	plan := Plan{
		WorktreeID: registration.WorktreeID, ProfileKey: registration.ProfileKey,
		WorktreeRoot: registration.WorktreeRoot, Ownership: registration.Ownership,
		Fingerprint: fingerprint, Steps: make([]StepSpec, 0, len(registration.Preparation.Steps)),
		Requirements: make([]Requirement, 0, len(registration.Preparation.Verify)), Toolchains: []Toolchain{},
	}
	for _, configured := range registration.Preparation.Steps {
		directory, valid := containedWorktreePath(registration.WorktreeRoot, configured.WorkingDirectory)
		if !valid {
			return Plan{}, ErrInvalidPlan
		}
		timeout, err := time.ParseDuration(configured.Timeout)
		if err != nil {
			return Plan{}, ErrInvalidPlan
		}
		environment := []string{
			"HOME=" + builder.home,
			"PATH=" + trustedExecutablePath(configured.Executable),
			"TMPDIR=" + builder.temporary,
		}
		for name, value := range configured.Environment {
			environment = append(environment, name+"="+value)
		}
		sort.Strings(environment)
		plan.Steps = append(plan.Steps, StepSpec{
			ID: configured.ID, Executable: configured.Executable,
			Arguments: append([]string(nil), configured.Arguments...), Environment: environment,
			Directory: directory,
			RunDirectory: filepath.Join(registration.RuntimeRoot, "repositories", registration.ProfileKey,
				registration.WorktreeID, "preparation", fingerprint, configured.ID),
			Timeout: timeout,
		})
	}
	for _, configured := range registration.Preparation.Verify {
		path, valid := containedWorktreePath(registration.WorktreeRoot, configured.Path)
		if !valid {
			return Plan{}, ErrInvalidPlan
		}
		plan.Requirements = append(plan.Requirements, Requirement{
			ID: configured.ID, Path: path, Kind: RequirementKind(configured.Kind),
		})
	}
	if validatePlan(plan) != nil {
		return Plan{}, ErrInvalidPlan
	}
	return plan, nil
}

func validProfileRegistration(registration ProfileRegistration) bool {
	if registration.WorktreeID == "" || registration.ProfileKey == "" || registration.ProfileDigest == "" ||
		!cleanAbsoluteDirectory(registration.WorktreeRoot) || !cleanAbsoluteDirectory(registration.RuntimeRoot) ||
		(registration.Ownership != OwnershipAdopted && registration.Ownership != OwnershipManaged) ||
		len(registration.Preparation.Steps) == 0 || len(registration.Preparation.Verify) == 0 {
		return false
	}
	for _, step := range registration.Preparation.Steps {
		info, err := os.Lstat(step.Executable)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
			return false
		}
	}
	return true
}

func cleanAbsoluteDirectory(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func containedWorktreePath(root, relative string) (string, bool) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return "", false
	}
	path := filepath.Join(root, relative)
	relativeToRoot, err := filepath.Rel(root, path)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", false
	}
	return path, true
}

func trustedExecutablePath(executable string) string {
	directories := []string{filepath.Dir(executable), "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	seen := make(map[string]struct{}, len(directories))
	unique := directories[:0]
	for _, directory := range directories {
		if _, exists := seen[directory]; !exists {
			seen[directory] = struct{}{}
			unique = append(unique, directory)
		}
	}
	return strings.Join(unique, string(os.PathListSeparator))
}

func profileFingerprint(registration ProfileRegistration) (string, error) {
	paths := append([]string(nil), registration.Preparation.Fingerprint.Files...)
	for _, pattern := range registration.Preparation.Fingerprint.Globs {
		matches, err := filepath.Glob(filepath.Join(registration.WorktreeRoot, pattern))
		if err != nil {
			return "", err
		}
		for _, match := range matches {
			relative, err := filepath.Rel(registration.WorktreeRoot, match)
			if err != nil {
				return "", err
			}
			paths = append(paths, relative)
		}
	}
	sort.Strings(paths)
	paths = compactStrings(paths)
	if len(paths) == 0 || len(paths) > maximumProfileFingerprintFiles {
		return "", ErrInvalidPlan
	}
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, "switchyard-profile-workspace-v1\x00"+registration.ProfileDigest+"\x00"+registration.WorktreeID+"\x00")
	for _, relative := range paths {
		path, valid := containedWorktreePath(registration.WorktreeRoot, relative)
		if !valid {
			return "", ErrInvalidPlan
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maximumProfileFingerprintBytes {
			return "", ErrInvalidPlan
		}
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hasher, filepath.ToSlash(relative)+"\x00")
		written, copyErr := io.CopyN(hasher, file, maximumProfileFingerprintBytes+1)
		closeErr := file.Close()
		if copyErr != nil && !errors.Is(copyErr, io.EOF) || closeErr != nil || written != info.Size() {
			return "", ErrInvalidPlan
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	output := values[:1]
	for _, value := range values[1:] {
		if value != output[len(output)-1] {
			output = append(output, value)
		}
	}
	return output
}

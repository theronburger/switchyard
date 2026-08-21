package profile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

// artifactTokenSchemaVersion 2 removed the verbatim content from the durable
// rollback token: a token now identifies each file by digest and size only.
const artifactTokenSchemaVersion = 2

const maximumPendingArtifactPlans = 256

type ArtifactMaterializer struct {
	registry Registry
	pending  *pendingArtifacts
}

// pendingArtifacts keeps resolved artifact content in memory between Plan and
// Apply. Resolved segments may embed metadata read from a worktree, so that
// content never enters the persisted rollback token or the operation journal.
type pendingArtifacts struct {
	mutex    sync.Mutex
	contents map[string]string
}

type artifactToken struct {
	SchemaVersion int            `json:"schemaVersion"`
	EnvironmentID string         `json:"environmentId"`
	RunID         string         `json:"runId"`
	Files         []artifactFile `json:"files"`
}

type artifactFile struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Digest     string `json:"digest"`
	SpecDigest string `json:"specDigest"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
}

func NewArtifactMaterializer(registry Registry) ArtifactMaterializer {
	return ArtifactMaterializer{registry: registry, pending: &pendingArtifacts{contents: make(map[string]string)}}
}

func pendingKey(environmentID, runID, artifactID, digest string) string {
	return environmentID + "\x00" + runID + "\x00" + artifactID + "\x00" + digest
}

func (pending *pendingArtifacts) put(key, content string) error {
	pending.mutex.Lock()
	defer pending.mutex.Unlock()
	if _, exists := pending.contents[key]; !exists && len(pending.contents) >= maximumPendingArtifactPlans {
		return ErrProfileInvalid
	}
	pending.contents[key] = content
	return nil
}

func (pending *pendingArtifacts) take(key string) (string, bool) {
	pending.mutex.Lock()
	defer pending.mutex.Unlock()
	content, found := pending.contents[key]
	delete(pending.contents, key)
	return content, found
}

func (pending *pendingArtifacts) drop(environmentID, runID string) {
	pending.mutex.Lock()
	defer pending.mutex.Unlock()
	prefix := environmentID + "\x00" + runID + "\x00"
	for key := range pending.contents {
		if strings.HasPrefix(key, prefix) {
			delete(pending.contents, key)
		}
	}
}

func (materializer ArtifactMaterializer) Plan(ctx context.Context, environmentID, runID string, request environmentcontrol.ProjectionRequest, leases []portlease.Lease) (environmentcontrol.ProjectionChange, error) {
	if err := ctx.Err(); err != nil {
		return environmentcontrol.ProjectionChange{}, err
	}
	registration, err := materializer.registry.Lookup(environmentID)
	if err != nil || request.ID != artifactProjectionID || runID == "" || len(request.ArtifactIDs) == 0 {
		return environmentcontrol.ProjectionChange{}, ErrProfileInvalid
	}
	runRoot := filepath.Join(registration.RuntimeRoot, "repositories", registration.ProfileKey, registration.WorktreeID,
		"environments", environmentID, "runs", runID)
	if materializer.pending == nil {
		return environmentcontrol.ProjectionChange{}, ErrProfileInvalid
	}
	token := artifactToken{SchemaVersion: artifactTokenSchemaVersion, EnvironmentID: environmentID, RunID: runID, Files: []artifactFile{}}
	seen := make(map[string]struct{}, len(request.ArtifactIDs))
	resolvedContents := make(map[string]string, len(request.ArtifactIDs))
	for _, id := range request.ArtifactIDs {
		artifact, found := registration.Profile.Artifacts[id]
		if !found {
			return environmentcontrol.ProjectionChange{}, ErrProfileInvalid
		}
		if _, duplicate := seen[id]; duplicate {
			return environmentcontrol.ProjectionChange{}, ErrProfileInvalid
		}
		seen[id] = struct{}{}
		filename := artifact.Filename
		if filename == "" {
			filename = id
		}
		content := artifact.Content
		if artifact.Segments != nil {
			resolved, err := resolveValues(registration, runRoot, "", artifact.Segments, nil, leaseMap(leases))
			if err != nil {
				return environmentcontrol.ProjectionChange{}, err
			}
			content = strings.Join(resolved, "")
		}
		sum := sha256.Sum256([]byte(content))
		specPayload, err := json.Marshal(artifact)
		if err != nil {
			return environmentcontrol.ProjectionChange{}, err
		}
		specSum := sha256.Sum256(specPayload)
		digest := hex.EncodeToString(sum[:])
		token.Files = append(token.Files, artifactFile{
			ID: id, Path: filepath.Join(runRoot, "artifacts", filename),
			Digest: digest, SpecDigest: hex.EncodeToString(specSum[:]),
			Size: int64(len(content)), Executable: artifact.Executable,
		})
		resolvedContents[pendingKey(environmentID, runID, id, digest)] = content
	}
	payload, err := json.Marshal(token)
	if err != nil || len(payload) > 1024*1024 {
		return environmentcontrol.ProjectionChange{}, ErrProfileInvalid
	}
	// Content is handed to Apply in memory only; the token that the journal
	// persists identifies every file by digest and size.
	for key, content := range resolvedContents {
		if err := materializer.pending.put(key, content); err != nil {
			materializer.pending.drop(environmentID, runID)
			return environmentcontrol.ProjectionChange{}, err
		}
	}
	return environmentcontrol.ProjectionChange{
		ID: request.ID, EnvironmentID: environmentID, RunID: runID,
		RollbackToken: base64.RawURLEncoding.EncodeToString(payload), Owned: true,
	}, nil
}

func (materializer ArtifactMaterializer) Apply(ctx context.Context, change environmentcontrol.ProjectionChange) error {
	token, _, err := materializer.validate(change)
	if err != nil {
		return err
	}
	if materializer.pending == nil {
		return ErrProfileInvalid
	}
	// Whatever happens, a plan's resolved content is consumed by exactly one
	// Apply; a later retry or rollback works from digests and the disk.
	defer materializer.pending.drop(change.EnvironmentID, change.RunID)
	for _, file := range token.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o700); err != nil {
			return err
		}
		if existingArtifactMatches(file) {
			continue
		}
		content, found := materializer.pending.take(pendingKey(change.EnvironmentID, change.RunID, file.ID, file.Digest))
		if !found || !contentMatches(file, content) {
			return ErrProfileInvalid
		}
		mode := os.FileMode(0o600)
		if file.Executable {
			mode = 0o700
		}
		descriptor, err := os.OpenFile(file.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
		if err != nil {
			return ErrProfileInvalid
		}
		_, writeErr := io.WriteString(descriptor, content)
		syncErr := descriptor.Sync()
		closeErr := descriptor.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			_ = os.Remove(file.Path)
			return errors.Join(writeErr, syncErr, closeErr)
		}
		if !existingArtifactMatches(file) {
			return ErrProfileInvalid
		}
	}
	return nil
}

func (materializer ArtifactMaterializer) Rollback(ctx context.Context, change environmentcontrol.ProjectionChange) error {
	token, _, err := materializer.validate(change)
	if err != nil {
		return err
	}
	for index := len(token.Files) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		file := token.Files[index]
		if _, err := os.Lstat(file.Path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if !existingArtifactMatches(file) {
			return ErrProfileInvalid
		}
		if err := os.Remove(file.Path); err != nil {
			return err
		}
		_ = os.Remove(filepath.Dir(file.Path))
	}
	return nil
}

func (materializer ArtifactMaterializer) validate(change environmentcontrol.ProjectionChange) (artifactToken, Registration, error) {
	if !change.Owned || change.ID != artifactProjectionID || change.EnvironmentID == "" || change.RunID == "" || len(change.RollbackToken) > 2*1024*1024 {
		return artifactToken{}, Registration{}, ErrProfileInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(change.RollbackToken)
	if err != nil || len(payload) > 1024*1024 {
		return artifactToken{}, Registration{}, ErrProfileInvalid
	}
	var token artifactToken
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&token) != nil {
		return artifactToken{}, Registration{}, ErrProfileInvalid
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || token.SchemaVersion != artifactTokenSchemaVersion ||
		token.EnvironmentID != change.EnvironmentID || token.RunID != change.RunID || len(token.Files) == 0 {
		return artifactToken{}, Registration{}, ErrProfileInvalid
	}
	registration, err := materializer.registry.Lookup(change.EnvironmentID)
	if err != nil {
		return artifactToken{}, Registration{}, err
	}
	runRoot := filepath.Join(registration.RuntimeRoot, "repositories", registration.ProfileKey, registration.WorktreeID,
		"environments", change.EnvironmentID, "runs", change.RunID, "artifacts")
	seen := make(map[string]struct{}, len(token.Files))
	for _, file := range token.Files {
		artifact, found := registration.Profile.Artifacts[file.ID]
		filename := artifact.Filename
		if filename == "" {
			filename = file.ID
		}
		specPayload, marshalErr := json.Marshal(artifact)
		specSum := sha256.Sum256(specPayload)
		// A static artifact's digest is fully determined by its spec; a segment
		// artifact is checked against the bytes on disk instead because its
		// resolved content is deliberately absent from the token.
		staticMismatch := artifact.Segments == nil && !contentMatches(file, artifact.Content)
		if !found || marshalErr != nil || staticMismatch || artifact.Executable != file.Executable ||
			file.SpecDigest != hex.EncodeToString(specSum[:]) || len(file.Digest) != sha256.Size*2 || file.Size < 0 ||
			file.Size > 2*1024*1024 || filepath.Dir(file.Path) != runRoot || filepath.Base(file.Path) != filename {
			return artifactToken{}, Registration{}, ErrProfileInvalid
		}
		if _, duplicate := seen[file.ID]; duplicate {
			return artifactToken{}, Registration{}, ErrProfileInvalid
		}
		seen[file.ID] = struct{}{}
	}
	return token, registration, nil
}

func leaseMap(leases []portlease.Lease) map[portlease.Key]portlease.Lease {
	result := make(map[portlease.Key]portlease.Lease, len(leases))
	for _, lease := range leases {
		result[lease.Key] = lease
	}
	return result
}

func existingArtifactMatches(file artifactFile) bool {
	descriptor, err := os.OpenFile(file.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer func() { _ = descriptor.Close() }()
	info, err := descriptor.Stat()
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	wantedMode := os.FileMode(0o600)
	if file.Executable {
		wantedMode = 0o700
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != wantedMode || !ok || stat.Nlink != 1 || info.Size() != file.Size {
		return false
	}
	contents, err := io.ReadAll(io.LimitReader(descriptor, file.Size+1))
	if err != nil || int64(len(contents)) != file.Size {
		return false
	}
	return contentMatches(file, string(contents))
}

// contentMatches reports whether content is exactly the bytes the token
// identifies by size and digest.
func contentMatches(file artifactFile, content string) bool {
	if int64(len(content)) != file.Size {
		return false
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:]) == file.Digest
}

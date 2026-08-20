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
	"syscall"

	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

const artifactTokenSchemaVersion = 1

type ArtifactMaterializer struct {
	registry Registry
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
	Content    string `json:"content"`
	Executable bool   `json:"executable"`
}

func NewArtifactMaterializer(registry Registry) ArtifactMaterializer {
	return ArtifactMaterializer{registry: registry}
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
	token := artifactToken{SchemaVersion: artifactTokenSchemaVersion, EnvironmentID: environmentID, RunID: runID, Files: []artifactFile{}}
	seen := make(map[string]struct{}, len(request.ArtifactIDs))
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
		token.Files = append(token.Files, artifactFile{
			ID: id, Path: filepath.Join(runRoot, "artifacts", filename),
			Digest: hex.EncodeToString(sum[:]), SpecDigest: hex.EncodeToString(specSum[:]),
			Content: content, Executable: artifact.Executable,
		})
	}
	payload, err := json.Marshal(token)
	if err != nil || len(payload) > 1024*1024 {
		return environmentcontrol.ProjectionChange{}, ErrProfileInvalid
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
		mode := os.FileMode(0o600)
		if file.Executable {
			mode = 0o700
		}
		descriptor, err := os.OpenFile(file.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
		if err != nil {
			return ErrProfileInvalid
		}
		_, writeErr := io.WriteString(descriptor, file.Content)
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
		sum := sha256.Sum256([]byte(file.Content))
		specPayload, marshalErr := json.Marshal(artifact)
		specSum := sha256.Sum256(specPayload)
		if !found || marshalErr != nil || (artifact.Segments == nil && artifact.Content != file.Content) || artifact.Executable != file.Executable ||
			file.SpecDigest != hex.EncodeToString(specSum[:]) ||
			file.Digest != hex.EncodeToString(sum[:]) || filepath.Dir(file.Path) != runRoot || filepath.Base(file.Path) != filename {
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
	if !info.Mode().IsRegular() || info.Mode().Perm() != wantedMode || !ok || stat.Nlink != 1 || info.Size() != int64(len(file.Content)) {
		return false
	}
	contents, err := io.ReadAll(io.LimitReader(descriptor, int64(len(file.Content))+1))
	if err != nil || len(contents) != len(file.Content) {
		return false
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]) == file.Digest
}

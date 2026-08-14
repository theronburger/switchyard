package marketplacecontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

const (
	projectionTokenSchemaVersion = 1
	maximumProjectionBytes       = 256 * 1024
	maximumRollbackTokenBytes    = 1024 * 1024
	projectionEnvironmentPrefix  = "// switchyard-environment-id: "
	projectionPayloadHashPrefix  = "// switchyard-payload-sha256: "
)

var (
	ErrProjectionInvalid  = errors.New("Marketplace projection request is invalid")
	ErrProjectionConflict = errors.New("Marketplace projection changed outside Switchyard ownership")
	ErrProjectionUnsafe   = errors.New("Marketplace projection path is unsafe")
)

type ProjectionApplier struct {
	registry EnvironmentRegistry
}

type projectionRollbackToken struct {
	SchemaVersion int                                               `json:"schemaVersion"`
	ProjectionID  string                                            `json:"projectionId"`
	EnvironmentID string                                            `json:"environmentId"`
	RunID         string                                            `json:"runId"`
	Action        marketplaceadapter.ServerlessProjectionEditAction `json:"action"`
	RelativePath  string                                            `json:"relativePath"`
	BeforeExists  bool                                              `json:"beforeExists"`
	Before        []byte                                            `json:"before,omitempty"`
	Desired       marketplaceadapter.OwnedServerlessProjection      `json:"desired"`
}

type projectionSnapshot struct {
	exists   bool
	contents []byte
}

func NewProjectionApplier(registry EnvironmentRegistry) (ProjectionApplier, error) {
	if len(registry.byEnvironment) == 0 {
		return ProjectionApplier{}, ErrProjectionInvalid
	}
	return ProjectionApplier{registry: registry}, nil
}

func (applier ProjectionApplier) Plan(
	ctx context.Context,
	environmentID string,
	runID string,
	request environment.ProjectionRequest,
	leases []portlease.Lease,
) (environment.ProjectionChange, error) {
	if err := ctx.Err(); err != nil {
		return environment.ProjectionChange{}, err
	}
	registration, err := applier.registry.Lookup(environmentID)
	if err != nil || request.ID != marketplaceServerlessProjection ||
		!registryIDPattern.MatchString(runID) {
		return environment.ProjectionChange{}, ErrProjectionInvalid
	}
	desired, err := renderProjection(environmentID, leases)
	if err != nil {
		return environment.ProjectionChange{}, ErrProjectionInvalid
	}
	root, err := openSafeProjectionRoot(registration.WorktreeRoot, desired.RelativePath)
	if err != nil {
		return environment.ProjectionChange{}, err
	}
	defer root.Close()
	current, err := readProjection(root, desired.RelativePath)
	if err != nil {
		return environment.ProjectionChange{}, err
	}
	if current.exists {
		ownerEnvironmentID, owned := projectionEnvironmentIdentity(current.contents)
		if !owned || ownerEnvironmentID != environmentID {
			return environment.ProjectionChange{}, ErrProjectionConflict
		}
	}
	edit, err := marketplaceadapter.PlanServerlessProjectionApply(
		marketplaceadapter.ExistingServerlessProjection{
			Exists: current.exists, Contents: current.contents,
		},
		desired,
	)
	if err != nil {
		return environment.ProjectionChange{}, ErrProjectionInvalid
	}
	if edit.Action == marketplaceadapter.ServerlessProjectionRefuse {
		return environment.ProjectionChange{}, ErrProjectionConflict
	}
	token, err := encodeProjectionToken(projectionRollbackToken{
		SchemaVersion: projectionTokenSchemaVersion,
		ProjectionID:  request.ID,
		EnvironmentID: environmentID,
		RunID:         runID,
		Action:        edit.Action,
		RelativePath:  desired.RelativePath,
		BeforeExists:  current.exists,
		Before:        bytes.Clone(current.contents),
		Desired:       desired,
	})
	if err != nil {
		return environment.ProjectionChange{}, ErrProjectionInvalid
	}
	return environment.ProjectionChange{
		ID:            request.ID,
		EnvironmentID: environmentID,
		RunID:         runID,
		RollbackToken: token,
		Owned:         true,
	}, nil
}

func (applier ProjectionApplier) Apply(ctx context.Context, change environment.ProjectionChange) error {
	token, registration, err := applier.validatedToken(change)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := openSafeProjectionRoot(registration.WorktreeRoot, token.RelativePath)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := readProjection(root, token.RelativePath)
	if err != nil {
		return err
	}
	if snapshotMatchesDesired(current, token.Desired) {
		return nil
	}
	if !snapshotMatchesBefore(current, token) {
		return ErrProjectionConflict
	}
	if token.Action == marketplaceadapter.ServerlessProjectionUnchanged {
		return nil
	}
	return writeProjectionCAS(ctx, root, token.RelativePath, current, token.Desired.Contents)
}

func (applier ProjectionApplier) Rollback(ctx context.Context, change environment.ProjectionChange) error {
	token, registration, err := applier.validatedToken(change)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := openSafeProjectionRoot(registration.WorktreeRoot, token.RelativePath)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := readProjection(root, token.RelativePath)
	if err != nil {
		return err
	}
	if snapshotMatchesBefore(current, token) {
		return nil
	}
	if !snapshotMatchesDesired(current, token.Desired) {
		return ErrProjectionConflict
	}
	if token.BeforeExists {
		return writeProjectionCAS(ctx, root, token.RelativePath, current, token.Before)
	}
	return removeProjectionCAS(ctx, root, token.RelativePath, current)
}

func (applier ProjectionApplier) validatedToken(
	change environment.ProjectionChange,
) (projectionRollbackToken, EnvironmentRegistration, error) {
	if !change.Owned || change.ID != marketplaceServerlessProjection {
		return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
	}
	token, err := decodeProjectionToken(change.RollbackToken)
	if err != nil || token.SchemaVersion != projectionTokenSchemaVersion ||
		token.ProjectionID != change.ID || token.EnvironmentID != change.EnvironmentID ||
		token.RunID != change.RunID || token.RelativePath != token.Desired.RelativePath {
		return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
	}
	desiredEnvironmentID, desiredOwned := projectionEnvironmentIdentity(token.Desired.Contents)
	if !desiredOwned || desiredEnvironmentID != token.EnvironmentID {
		return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
	}
	if token.BeforeExists {
		beforeEnvironmentID, beforeOwned := projectionEnvironmentIdentity(token.Before)
		if !beforeOwned || beforeEnvironmentID != token.EnvironmentID {
			return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
		}
	}
	registration, err := applier.registry.Lookup(change.EnvironmentID)
	if err != nil {
		return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
	}
	planned, err := marketplaceadapter.PlanServerlessProjectionApply(
		marketplaceadapter.ExistingServerlessProjection{
			Exists:   token.BeforeExists,
			Contents: bytes.Clone(token.Before),
		},
		token.Desired,
	)
	if err != nil || planned.Action != token.Action ||
		planned.RelativePath != token.RelativePath {
		return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
	}
	return token, registration, nil
}

func renderProjection(
	environmentID string,
	leases []portlease.Lease,
) (marketplaceadapter.OwnedServerlessProjection, error) {
	definition, found := marketplaceadapter.DefaultCatalog().Definition("nonprofit-service")
	if !found || definition.ServerlessOverlay == nil {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	byPurpose := make(map[string]portlease.Lease)
	for _, lease := range leases {
		if lease.Key.EnvironmentID != environmentID || lease.Host != "127.0.0.1" ||
			lease.Port < 1 || lease.Port > 65535 {
			return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
		}
		if lease.Key.ServiceID != "nonprofit-service" {
			continue
		}
		if _, duplicate := byPurpose[lease.Key.Purpose]; duplicate {
			return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
		}
		byPurpose[lease.Key.Purpose] = lease
	}
	ports := make([]marketplaceadapter.PortAssignment, 0, len(definition.PortRequirements))
	for _, requirement := range definition.PortRequirements {
		lease, found := byPurpose[requirement.Purpose]
		if !found {
			return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
		}
		ports = append(ports, marketplaceadapter.PortAssignment{
			RequirementID: requirement.ID,
			Host:          lease.Host,
			Port:          lease.Port,
		})
	}
	projection, err := marketplaceadapter.RenderServerlessProjection(*definition.ServerlessOverlay, ports)
	if err != nil {
		return marketplaceadapter.OwnedServerlessProjection{}, err
	}
	return scopeProjectionToEnvironment(projection, environmentID)
}

func scopeProjectionToEnvironment(
	projection marketplaceadapter.OwnedServerlessProjection,
	environmentID string,
) (marketplaceadapter.OwnedServerlessProjection, error) {
	if !registryIDPattern.MatchString(environmentID) {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	header, payload, found := bytes.Cut(projection.Contents, []byte("\n\n"))
	headerLines := bytes.Split(header, []byte{'\n'})
	if !found || len(headerLines) != 2 ||
		!bytes.HasPrefix(headerLines[1], []byte(projectionPayloadHashPrefix)) {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	const commonJSLoad = "const configuration = require(\"./serverless.ts\")\n"
	const compatibleLoad = "const importedConfiguration = require(\"./serverless.ts\")\n" +
		"const configuration = importedConfiguration.default ?? importedConfiguration\n"
	if bytes.Count(payload, []byte(commonJSLoad)) != 1 {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	payload = bytes.Replace(payload, []byte(commonJSLoad), []byte(compatibleLoad), 1)
	payload = append(
		[]byte(projectionEnvironmentPrefix+strconv.Quote(environmentID)+"\n"),
		payload...,
	)
	payloadHash := sha256.Sum256(payload)
	payloadSHA256 := hex.EncodeToString(payloadHash[:])
	contents := make([]byte, 0, len(headerLines[0])+len(payload)+96)
	contents = append(contents, headerLines[0]...)
	contents = append(contents, '\n')
	contents = append(contents, projectionPayloadHashPrefix...)
	contents = append(contents, payloadSHA256...)
	contents = append(contents, '\n', '\n')
	contents = append(contents, payload...)
	contentHash := sha256.Sum256(contents)
	projection.Contents = contents
	projection.PayloadSHA256 = payloadSHA256
	projection.ContentSHA256 = hex.EncodeToString(contentHash[:])
	return projection, nil
}

func projectionEnvironmentIdentity(contents []byte) (string, bool) {
	_, payload, found := bytes.Cut(contents, []byte("\n\n"))
	if !found {
		return "", false
	}
	line, _, found := bytes.Cut(payload, []byte{'\n'})
	if !found || !bytes.HasPrefix(line, []byte(projectionEnvironmentPrefix)) {
		return "", false
	}
	environmentID, err := strconv.Unquote(strings.TrimPrefix(string(line), projectionEnvironmentPrefix))
	return environmentID, err == nil && registryIDPattern.MatchString(environmentID)
}

func encodeProjectionToken(token projectionRollbackToken) (string, error) {
	payload, err := json.Marshal(token)
	if err != nil || len(payload) > maximumRollbackTokenBytes {
		return "", ErrProjectionInvalid
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeProjectionToken(encoded string) (projectionRollbackToken, error) {
	if encoded == "" || len(encoded) > base64.RawURLEncoding.EncodedLen(maximumRollbackTokenBytes) {
		return projectionRollbackToken{}, ErrProjectionInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) > maximumRollbackTokenBytes {
		return projectionRollbackToken{}, ErrProjectionInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var token projectionRollbackToken
	if decoder.Decode(&token) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return projectionRollbackToken{}, ErrProjectionInvalid
	}
	return token, nil
}

func openSafeProjectionRoot(rootPath, relativePath string) (*os.Root, error) {
	rootInfo, err := os.Lstat(rootPath)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrProjectionUnsafe
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, ErrProjectionUnsafe
	}
	directory := filepath.Dir(relativePath)
	current := ""
	for _, segment := range strings.Split(directory, string(filepath.Separator)) {
		if segment == "" || segment == "." || segment == ".." {
			root.Close()
			return nil, ErrProjectionUnsafe
		}
		current = filepath.Join(current, segment)
		info, err := root.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, ErrProjectionUnsafe
		}
	}
	return root, nil
}

func readProjection(root *os.Root, relativePath string) (projectionSnapshot, error) {
	file, err := root.OpenFile(relativePath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return projectionSnapshot{}, nil
	}
	if err != nil {
		return projectionSnapshot{}, ErrProjectionUnsafe
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 ||
		before.Size() < 0 || before.Size() > maximumProjectionBytes {
		return projectionSnapshot{}, ErrProjectionUnsafe
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumProjectionBytes+1))
	if err != nil || len(contents) > maximumProjectionBytes {
		return projectionSnapshot{}, ErrProjectionUnsafe
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || int64(len(contents)) != before.Size() {
		return projectionSnapshot{}, ErrProjectionUnsafe
	}
	return projectionSnapshot{exists: true, contents: contents}, nil
}

func writeProjectionCAS(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	expected projectionSnapshot,
	contents []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(contents) > maximumProjectionBytes {
		return ErrProjectionInvalid
	}
	temporaryPath, err := temporaryProjectionPath(relativePath)
	if err != nil {
		return ErrProjectionInvalid
	}
	file, err := root.OpenFile(
		temporaryPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return ErrProjectionConflict
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = root.Remove(temporaryPath)
		}
	}()
	if err := writeAndSync(file, contents); err != nil {
		return ErrProjectionUnsafe
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := readProjection(root, relativePath)
	if err != nil {
		return err
	}
	if !snapshotsEqual(current, expected) {
		return ErrProjectionConflict
	}
	if !expected.exists {
		if err := root.Link(temporaryPath, relativePath); err != nil {
			return ErrProjectionConflict
		}
		if err := root.Remove(temporaryPath); err != nil {
			return ErrProjectionUnsafe
		}
		removeTemporary = false
	} else {
		if err := root.Rename(temporaryPath, relativePath); err != nil {
			return ErrProjectionConflict
		}
		removeTemporary = false
	}
	return syncProjectionDirectory(root, relativePath)
}

func removeProjectionCAS(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	expected projectionSnapshot,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := readProjection(root, relativePath)
	if err != nil {
		return err
	}
	if !snapshotsEqual(current, expected) {
		return ErrProjectionConflict
	}
	if err := root.Remove(relativePath); err != nil {
		return ErrProjectionConflict
	}
	return syncProjectionDirectory(root, relativePath)
}

func writeAndSync(file *os.File, contents []byte) error {
	written := 0
	for written < len(contents) {
		count, err := file.Write(contents[written:])
		if err != nil {
			_ = file.Close()
			return err
		}
		written += count
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func temporaryProjectionPath(relativePath string) (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return filepath.Join(
		filepath.Dir(relativePath),
		".switchyard-projection-"+hex.EncodeToString(randomBytes),
	), nil
}

func syncProjectionDirectory(root *os.Root, relativePath string) error {
	directory, err := root.Open(filepath.Dir(relativePath))
	if err != nil {
		return ErrProjectionUnsafe
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return ErrProjectionUnsafe
	}
	return nil
}

func snapshotMatchesBefore(snapshot projectionSnapshot, token projectionRollbackToken) bool {
	return snapshot.exists == token.BeforeExists &&
		(!snapshot.exists || bytes.Equal(snapshot.contents, token.Before))
}

func snapshotMatchesDesired(
	snapshot projectionSnapshot,
	desired marketplaceadapter.OwnedServerlessProjection,
) bool {
	return snapshot.exists && bytes.Equal(snapshot.contents, desired.Contents)
}

func snapshotsEqual(left, right projectionSnapshot) bool {
	return left.exists == right.exists && (!left.exists || bytes.Equal(left.contents, right.contents))
}

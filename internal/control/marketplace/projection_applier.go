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
	"sort"
	"strconv"
	"strings"
	"syscall"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

const (
	projectionTokenSchemaVersion = 2
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
	SchemaVersion int                       `json:"schemaVersion"`
	ProjectionID  string                    `json:"projectionId"`
	EnvironmentID string                    `json:"environmentId"`
	RunID         string                    `json:"runId"`
	Entries       []projectionRollbackEntry `json:"entries"`
}

type projectionRollbackEntry struct {
	Action       marketplaceadapter.ServerlessProjectionEditAction `json:"action"`
	RelativePath string                                            `json:"relativePath"`
	BeforeExists bool                                              `json:"beforeExists"`
	Before       []byte                                            `json:"before,omitempty"`
	Desired      marketplaceadapter.OwnedServerlessProjection      `json:"desired"`
}

type projectionSnapshot struct {
	exists   bool
	contents []byte
}

type endpointRewrite struct {
	FromHost string `json:"fromHost"`
	FromPort int    `json:"fromPort"`
	ToHost   string `json:"toHost"`
	ToPort   int    `json:"toPort"`
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
	desiredProjections, err := renderProjections(environmentID, registration.RepositoryRoot, leases)
	if err != nil {
		return environment.ProjectionChange{}, ErrProjectionInvalid
	}
	if len(desiredProjections) == 0 {
		return environment.ProjectionChange{}, ErrProjectionInvalid
	}
	entries := make([]projectionRollbackEntry, 0, len(desiredProjections))
	for _, desired := range desiredProjections {
		root, openError := openSafeProjectionRoot(registration.WorktreeRoot, desired.RelativePath)
		if openError != nil {
			return environment.ProjectionChange{}, openError
		}
		current, readError := readProjection(root, desired.RelativePath)
		_ = root.Close()
		if readError != nil {
			return environment.ProjectionChange{}, readError
		}
		if current.exists {
			ownerEnvironmentID, owned := projectionEnvironmentIdentity(current.contents)
			if !owned || ownerEnvironmentID != environmentID {
				return environment.ProjectionChange{}, ErrProjectionConflict
			}
		}
		edit, planError := marketplaceadapter.PlanServerlessProjectionApply(
			marketplaceadapter.ExistingServerlessProjection{Exists: current.exists, Contents: current.contents},
			desired,
		)
		if planError != nil {
			return environment.ProjectionChange{}, ErrProjectionInvalid
		}
		if edit.Action == marketplaceadapter.ServerlessProjectionRefuse {
			return environment.ProjectionChange{}, ErrProjectionConflict
		}
		entries = append(entries, projectionRollbackEntry{
			Action: edit.Action, RelativePath: desired.RelativePath, BeforeExists: current.exists,
			Before: bytes.Clone(current.contents), Desired: desired,
		})
	}
	token, err := encodeProjectionToken(projectionRollbackToken{
		SchemaVersion: projectionTokenSchemaVersion,
		ProjectionID:  request.ID,
		EnvironmentID: environmentID,
		RunID:         runID,
		Entries:       entries,
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
	for _, entry := range token.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		root, openError := openSafeProjectionRoot(registration.WorktreeRoot, entry.RelativePath)
		if openError != nil {
			return openError
		}
		current, readError := readProjection(root, entry.RelativePath)
		if readError != nil {
			_ = root.Close()
			return readError
		}
		if snapshotMatchesDesired(current, entry.Desired) {
			_ = root.Close()
			continue
		}
		if !snapshotMatchesBefore(current, entry) {
			_ = root.Close()
			return ErrProjectionConflict
		}
		if entry.Action == marketplaceadapter.ServerlessProjectionUnchanged {
			_ = root.Close()
			continue
		}
		writeError := writeProjectionCAS(ctx, root, entry.RelativePath, current, entry.Desired.Contents)
		_ = root.Close()
		if writeError != nil {
			return writeError
		}
	}
	return nil
}

func (applier ProjectionApplier) Rollback(ctx context.Context, change environment.ProjectionChange) error {
	token, registration, err := applier.validatedToken(change)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for index := len(token.Entries) - 1; index >= 0; index-- {
		entry := token.Entries[index]
		if err := ctx.Err(); err != nil {
			return err
		}
		root, openError := openSafeProjectionRoot(registration.WorktreeRoot, entry.RelativePath)
		if openError != nil {
			return openError
		}
		current, readError := readProjection(root, entry.RelativePath)
		if readError != nil {
			_ = root.Close()
			return readError
		}
		if snapshotMatchesBefore(current, entry) {
			_ = root.Close()
			continue
		}
		if !snapshotMatchesDesired(current, entry.Desired) {
			_ = root.Close()
			return ErrProjectionConflict
		}
		var rollbackError error
		if entry.BeforeExists {
			rollbackError = writeProjectionCAS(ctx, root, entry.RelativePath, current, entry.Before)
		} else {
			rollbackError = removeProjectionCAS(ctx, root, entry.RelativePath, current)
		}
		_ = root.Close()
		if rollbackError != nil {
			return rollbackError
		}
	}
	return nil
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
		token.RunID != change.RunID || len(token.Entries) == 0 {
		return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
	}
	seenPaths := make(map[string]struct{}, len(token.Entries))
	for _, entry := range token.Entries {
		if entry.RelativePath != entry.Desired.RelativePath {
			return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
		}
		if _, duplicate := seenPaths[entry.RelativePath]; duplicate {
			return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
		}
		seenPaths[entry.RelativePath] = struct{}{}
		desiredEnvironmentID, desiredOwned := projectionEnvironmentIdentity(entry.Desired.Contents)
		if !desiredOwned || desiredEnvironmentID != token.EnvironmentID {
			return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
		}
		if entry.BeforeExists {
			beforeEnvironmentID, beforeOwned := projectionEnvironmentIdentity(entry.Before)
			if !beforeOwned || beforeEnvironmentID != token.EnvironmentID {
				return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
			}
		}
		planned, planError := marketplaceadapter.PlanServerlessProjectionApply(
			marketplaceadapter.ExistingServerlessProjection{Exists: entry.BeforeExists, Contents: bytes.Clone(entry.Before)},
			entry.Desired,
		)
		if planError != nil || planned.Action != entry.Action || planned.RelativePath != entry.RelativePath {
			return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
		}
	}
	registration, err := applier.registry.Lookup(change.EnvironmentID)
	if err != nil {
		return projectionRollbackToken{}, EnvironmentRegistration{}, ErrProjectionInvalid
	}
	return token, registration, nil
}

func renderProjection(
	environmentID string,
	repositoryRoot string,
	leases []portlease.Lease,
) (marketplaceadapter.OwnedServerlessProjection, error) {
	projections, err := renderProjections(environmentID, repositoryRoot, leases)
	if err != nil {
		return marketplaceadapter.OwnedServerlessProjection{}, err
	}
	for _, projection := range projections {
		if projection.RelativePath == "services/nonprofit-service/.switchyard.serverless.ts" {
			return projection, nil
		}
	}
	return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
}

func renderProjections(
	environmentID string,
	repositoryRoot string,
	leases []portlease.Lease,
) ([]marketplaceadapter.OwnedServerlessProjection, error) {
	if !cleanAbsolutePath(repositoryRoot) || filepath.Dir(repositoryRoot) == repositoryRoot {
		return nil, ErrProjectionInvalid
	}
	byService := make(map[string]map[string]portlease.Lease)
	for _, lease := range leases {
		if lease.Key.EnvironmentID != environmentID || lease.Host != "127.0.0.1" ||
			lease.Port < 1 || lease.Port > 65535 {
			return nil, ErrProjectionInvalid
		}
		byPurpose := byService[lease.Key.ServiceID]
		if byPurpose == nil {
			byPurpose = make(map[string]portlease.Lease)
			byService[lease.Key.ServiceID] = byPurpose
		}
		if _, duplicate := byPurpose[lease.Key.Purpose]; duplicate {
			return nil, ErrProjectionInvalid
		}
		byPurpose[lease.Key.Purpose] = lease
	}
	serviceIDs := make([]string, 0, len(byService))
	for serviceID := range byService {
		serviceIDs = append(serviceIDs, serviceID)
	}
	sort.Strings(serviceIDs)
	catalog := marketplaceadapter.DefaultCatalog()
	environmentShim, err := renderEnvironmentShim(environmentID, repositoryRoot)
	if err != nil {
		return nil, err
	}
	projections := make([]marketplaceadapter.OwnedServerlessProjection, 0, len(serviceIDs)+2)
	projections = append(projections, environmentShim)
	for _, serviceID := range serviceIDs {
		definition, found := catalog.Definition(serviceID)
		if !found || definition.ServerlessOverlay == nil {
			continue
		}
		ports := make([]marketplaceadapter.PortAssignment, 0, len(definition.PortRequirements))
		for _, requirement := range definition.PortRequirements {
			lease, assigned := byService[serviceID][requirement.Purpose]
			if !assigned {
				return nil, ErrProjectionInvalid
			}
			ports = append(ports, marketplaceadapter.PortAssignment{
				RequirementID: requirement.ID, Host: lease.Host, Port: lease.Port,
			})
		}
		projection, renderError := marketplaceadapter.RenderServerlessProjection(*definition.ServerlessOverlay, ports)
		if renderError != nil {
			return nil, ErrProjectionInvalid
		}
		projection, renderError = scopeProjectionToEnvironment(projection, environmentID)
		if renderError != nil {
			return nil, renderError
		}
		projections = append(projections, projection)
		switch serviceID {
		case "donation-batch-service":
			elasticMQLease, assigned := byService[serviceID]["elasticmq-rest"]
			if !assigned {
				return nil, ErrProjectionInvalid
			}
			shim, shimError := renderEndpointShim(
				environmentID,
				"services/donation-batch-service/.switchyard.endpoints.cjs",
				[]endpointRewrite{
					{FromHost: "0.0.0.0", FromPort: 9324, ToHost: "127.0.0.1", ToPort: elasticMQLease.Port},
					{FromHost: "localhost", FromPort: 9324, ToHost: "127.0.0.1", ToPort: elasticMQLease.Port},
					{FromHost: "127.0.0.1", FromPort: 9324, ToHost: "127.0.0.1", ToPort: elasticMQLease.Port},
				},
			)
			if shimError != nil {
				return nil, shimError
			}
			projections = append(projections, shim)
		case "slack-service":
			dynamoDBLease, assigned := byService[serviceID]["dynamodb"]
			if !assigned {
				return nil, ErrProjectionInvalid
			}
			shim, shimError := renderEndpointShim(
				environmentID,
				"services/slack-service/.switchyard.endpoints.cjs",
				[]endpointRewrite{
					{FromHost: "localhost", FromPort: 8000, ToHost: "127.0.0.1", ToPort: dynamoDBLease.Port},
					{FromHost: "127.0.0.1", FromPort: 8000, ToHost: "127.0.0.1", ToPort: dynamoDBLease.Port},
				},
			)
			if shimError != nil {
				return nil, shimError
			}
			projections = append(projections, shim)
		}
	}
	sort.Slice(projections, func(left, right int) bool {
		return projections[left].RelativePath < projections[right].RelativePath
	})
	for index := 1; index < len(projections); index++ {
		if projections[index-1].RelativePath == projections[index].RelativePath {
			return nil, ErrProjectionInvalid
		}
	}
	return projections, nil
}

func renderEnvironmentShim(
	environmentID string,
	repositoryRoot string,
) (marketplaceadapter.OwnedServerlessProjection, error) {
	if !cleanAbsolutePath(repositoryRoot) || filepath.Dir(repositoryRoot) == repositoryRoot {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	payload := `"use strict"

const path = require("node:path")
const root = __dirname

const dotenvFlow = require("dotenv-flow")
const dotenvExpand = require("dotenv-expand")

// Linked worktrees do not contain the primary checkout's ignored local target
// overlays. Load them by reference before Marketplace's worktree-local loader;
// existing Switchyard-owned routing values still win because dotenv does not
// replace values already present in process.env.
const sharedLocalRoot = ` + strconv.Quote(repositoryRoot) + `
const targetProfile = dotenvFlow.config({
  path: sharedLocalRoot,
  node_env: process.env.DEPLOYMENT_ENVIRONMENT || process.env.NODE_ENV,
  default_node_env: "development",
})
if (targetProfile.error) throw targetProfile.error
dotenvExpand.expand(targetProfile)

// Marketplace's loader applies the selected target first. The development
// profile is then a missing-value fallback for local-only runtime prerequisites.
require(path.join(root, ".env.js"))
const localFallback = dotenvFlow.config({
  path: root,
  node_env: "development",
  default_node_env: "development",
})
if (localFallback.error) throw localFallback.error
dotenvExpand.expand(localFallback)
`
	return ownedRuntimeProjection(
		marketplaceEnvironmentShim,
		environmentID,
		[]byte(payload),
	)
}

func renderEndpointShim(
	environmentID string,
	relativePath string,
	rewrites []endpointRewrite,
) (marketplaceadapter.OwnedServerlessProjection, error) {
	if len(rewrites) == 0 {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	seen := make(map[string]struct{}, len(rewrites))
	for _, rewrite := range rewrites {
		key := rewrite.FromHost + ":" + strconv.Itoa(rewrite.FromPort)
		if (rewrite.FromHost != "localhost" && rewrite.FromHost != "127.0.0.1" && rewrite.FromHost != "0.0.0.0") ||
			rewrite.ToHost != "127.0.0.1" || rewrite.FromPort < 1 || rewrite.FromPort > 65535 ||
			rewrite.ToPort < 1 || rewrite.ToPort > 65535 {
			return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
		}
		if _, duplicate := seen[key]; duplicate {
			return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
		}
		seen[key] = struct{}{}
	}
	encodedRules, err := json.Marshal(rewrites)
	if err != nil {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	payload := `const http = require("node:http")
const originalRequest = http.request
const rewrites = new Map(` + string(encodedRules) + `.map(rule => [` + "`${rule.fromHost}:${rule.fromPort}`" + `, rule]))

function rewriteOptions(options) {
  if (!options || typeof options !== "object") return options
  let hostname = options.hostname ?? options.host
  let port = Number(options.port ?? 0)
  if (!port && typeof hostname === "string") {
    const match = hostname.match(/^([^:]+):(\d+)$/)
    if (match) { hostname = match[1]; port = Number(match[2]) }
  }
  const rewrite = rewrites.get(` + "`${hostname}:${port}`" + `)
  if (!rewrite) return options
  return { ...options, host: rewrite.toHost, hostname: rewrite.toHost, port: rewrite.toPort }
}

function rewriteInput(input) {
  if (typeof input !== "string" && !(input instanceof URL)) return rewriteOptions(input)
  let url
  try { url = new URL(input) } catch { return input }
  const rewrite = rewrites.get(` + "`${url.hostname}:${Number(url.port)}`" + `)
  if (!rewrite || url.protocol !== "http:") return input
  url.hostname = rewrite.toHost
  url.port = String(rewrite.toPort)
  return typeof input === "string" ? url.toString() : url
}

http.request = function switchyardRequest(input, options, callback) {
  if (typeof options === "function") return originalRequest.call(this, rewriteInput(input), options)
  return originalRequest.call(this, rewriteInput(input), rewriteOptions(options), callback)
}

http.get = function switchyardGet(input, options, callback) {
  const request = http.request(input, options, callback)
  request.end()
  return request
}
`
	return ownedRuntimeProjection(
		relativePath,
		environmentID,
		[]byte(payload),
	)
}

func ownedRuntimeProjection(
	relativePath string,
	environmentID string,
	payload []byte,
) (marketplaceadapter.OwnedServerlessProjection, error) {
	if !registryIDPattern.MatchString(environmentID) {
		return marketplaceadapter.OwnedServerlessProjection{}, ErrProjectionInvalid
	}
	scopedPayload := append([]byte(projectionEnvironmentPrefix+strconv.Quote(environmentID)+"\n"), payload...)
	payloadHash := sha256.Sum256(scopedPayload)
	payloadSHA256 := hex.EncodeToString(payloadHash[:])
	contents := []byte("// switchyard-owner: switchyard.marketplace.serverless.v1\n" +
		projectionPayloadHashPrefix + payloadSHA256 + "\n\n")
	contents = append(contents, scopedPayload...)
	contentHash := sha256.Sum256(contents)
	return marketplaceadapter.OwnedServerlessProjection{
		RelativePath: relativePath, Contents: contents, PayloadSHA256: payloadSHA256,
		ContentSHA256: hex.EncodeToString(contentHash[:]),
	}, nil
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
	if directory == "." {
		return root, nil
	}
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

func snapshotMatchesBefore(snapshot projectionSnapshot, entry projectionRollbackEntry) bool {
	return snapshot.exists == entry.BeforeExists &&
		(!snapshot.exists || bytes.Equal(snapshot.contents, entry.Before))
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

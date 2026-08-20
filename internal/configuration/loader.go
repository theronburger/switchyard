package configuration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const (
	maximumConfigurationBytes = 2 << 20
	maximumYAMLDepth          = 32
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func LoadFile(path string) (Loaded, error) {
	contents, err := readPrivateRegularFile(path)
	if err != nil {
		return Loaded{}, err
	}
	loaded, err := Parse(contents)
	if err != nil {
		return Loaded{}, fmt.Errorf("parse private configuration: %w", err)
	}
	return loaded, nil
}

func Parse(contents []byte) (Loaded, error) {
	if len(contents) == 0 {
		return Loaded{}, errors.New("configuration is empty")
	}
	if len(contents) > maximumConfigurationBytes {
		return Loaded{}, fmt.Errorf("configuration exceeds %d bytes", maximumConfigurationBytes)
	}
	if bytes.IndexByte(contents, 0) >= 0 {
		return Loaded{}, errors.New("configuration contains a NUL byte")
	}

	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(&root); err != nil {
		return Loaded{}, fmt.Errorf("decode YAML: %w", err)
	}
	if err := validateYAMLNode(&root, 0); err != nil {
		return Loaded{}, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Loaded{}, errors.New("configuration must contain exactly one YAML document")
		}
		return Loaded{}, fmt.Errorf("decode trailing YAML: %w", err)
	}

	var document Document
	strict := yaml.NewDecoder(bytes.NewReader(contents))
	strict.KnownFields(true)
	if err := strict.Decode(&document); err != nil {
		return Loaded{}, fmt.Errorf("decode schema: %w", err)
	}
	if err := validateDocument(document); err != nil {
		return Loaded{}, err
	}

	payload, err := json.Marshal(document)
	if err != nil {
		return Loaded{}, fmt.Errorf("canonicalize configuration: %w", err)
	}
	executableDigests, repositoryExecutables, err := fingerprintExecutables(document)
	if err != nil {
		return Loaded{}, err
	}
	repositoryDigests := make(map[string]string, len(document.Repositories))
	for _, key := range sortedRepositoryKeys(document.Repositories) {
		repositoryPayload, err := json.Marshal(document.Repositories[key])
		if err != nil {
			return Loaded{}, fmt.Errorf("canonicalize repository %q: %w", key, err)
		}
		identity, err := json.Marshal(struct {
			Profile     json.RawMessage   `json:"profile"`
			Executables map[string]string `json:"executables"`
		}{Profile: repositoryPayload, Executables: repositoryExecutables[key]})
		if err != nil {
			return Loaded{}, fmt.Errorf("canonicalize repository identity %q: %w", key, err)
		}
		repositoryDigests[key] = digest(identity)
	}
	identity, err := json.Marshal(struct {
		Configuration json.RawMessage   `json:"configuration"`
		Executables   map[string]string `json:"executables"`
	}{Configuration: payload, Executables: executableDigests})
	if err != nil {
		return Loaded{}, fmt.Errorf("canonicalize execution identity: %w", err)
	}
	return Loaded{
		Document: document, CanonicalPayload: payload, Digest: digest(identity),
		SourceDigest: digest(contents), RepositoryDigests: repositoryDigests, ExecutableDigests: executableDigests,
	}, nil
}

func fingerprintExecutables(document Document) (map[string]string, map[string]map[string]string, error) {
	all := make(map[string]string)
	byRepository := make(map[string]map[string]string, len(document.Repositories))
	for key, repository := range document.Repositories {
		paths := make(map[string]struct{})
		for _, step := range repository.Preparation.Steps {
			paths[step.Executable] = struct{}{}
		}
		for _, toolchain := range repository.Toolchains {
			if toolchain.Executable != "" {
				paths[toolchain.Executable] = struct{}{}
			}
			if toolchain.Provision != nil {
				paths[toolchain.Provision.Executable] = struct{}{}
			}
		}
		for _, service := range repository.Services {
			if service.IsAvailable() {
				paths[service.Command.Executable] = struct{}{}
			}
			for _, command := range service.Prepare {
				paths[command.Executable] = struct{}{}
			}
			for _, command := range service.Initialize {
				paths[command.Executable] = struct{}{}
			}
		}
		for _, action := range repository.Actions {
			if action.Command != nil {
				paths[action.Command.Executable] = struct{}{}
			}
		}
		digests := make(map[string]string, len(paths))
		ordered := make([]string, 0, len(paths))
		for path := range paths {
			if path != "" {
				ordered = append(ordered, path)
			}
		}
		sort.Strings(ordered)
		for _, path := range ordered {
			fingerprint, found := all[path]
			if !found {
				var err error
				fingerprint, err = fingerprintExecutable(path)
				if err != nil {
					return nil, nil, fmt.Errorf("fingerprint repository %q executable: %w", key, err)
				}
				all[path] = fingerprint
			}
			digests[path] = fingerprint
		}
		byRepository[key] = digests
	}
	return all, byRepository, nil
}

func fingerprintExecutable(path string) (string, error) {
	if !cleanAbsolutePath(path) {
		return "", errors.New("executable path is invalid")
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return "", errors.New("open executable")
	}
	defer func() { _ = file.Close() }()
	var metadata unix.Stat_t
	if err := unix.Fstat(descriptor, &metadata); err != nil {
		return "", err
	}
	if metadata.Mode&unix.S_IFMT != unix.S_IFREG || metadata.Mode&0o111 == 0 || metadata.Size < 0 || metadata.Size > 512*1024*1024 {
		return "", errors.New("executable is not a bounded regular executable file")
	}
	hasher := sha256.New()
	if _, err := io.CopyN(hasher, file, metadata.Size); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func readPrivateRegularFile(path string) ([]byte, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean != path {
		return nil, errors.New("configuration path must be clean and absolute")
	}
	parentInfo, err := os.Lstat(filepath.Dir(clean))
	if err != nil {
		return nil, fmt.Errorf("inspect configuration directory: %w", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode().Perm() != 0o700 {
		return nil, errors.New("configuration directory must be a private directory with mode 0700")
	}

	descriptor, err := unix.Open(clean, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open configuration without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), clean)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("open configuration file")
	}
	defer func() { _ = file.Close() }()

	var metadata unix.Stat_t
	if err := unix.Fstat(descriptor, &metadata); err != nil {
		return nil, fmt.Errorf("inspect configuration file: %w", err)
	}
	if metadata.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("configuration must be a regular file")
	}
	if metadata.Mode&0o777 != 0o600 {
		return nil, errors.New("configuration file must have mode 0600")
	}
	if int(metadata.Uid) != os.Getuid() {
		return nil, errors.New("configuration file must be owned by the current user")
	}
	if metadata.Size > maximumConfigurationBytes {
		return nil, fmt.Errorf("configuration exceeds %d bytes", maximumConfigurationBytes)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumConfigurationBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	if len(contents) > maximumConfigurationBytes {
		return nil, fmt.Errorf("configuration exceeds %d bytes", maximumConfigurationBytes)
	}
	return contents, nil
}

func validateYAMLNode(node *yaml.Node, depth int) error {
	if depth > maximumYAMLDepth {
		return fmt.Errorf("YAML nesting exceeds %d levels", maximumYAMLDepth)
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode || node.Alias != nil {
		return errors.New("YAML anchors and aliases are not allowed")
	}
	allowedTags := map[string]bool{
		"": true, "!!map": true, "!!seq": true, "!!str": true,
		"!!int": true, "!!bool": true, "!!float": true, "!!null": true,
	}
	if !allowedTags[node.Tag] {
		return fmt.Errorf("YAML tag %q is not allowed", node.Tag)
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("YAML mapping keys must be strings")
			}
			if key.Value == "<<" {
				return errors.New("YAML merge keys are not allowed")
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("duplicate YAML mapping key %q", key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateDocument(document Document) error {
	if document.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported configuration schema version %d", document.SchemaVersion)
	}
	if document.Machine.Ports.First < 1024 || document.Machine.Ports.Last > 65535 ||
		document.Machine.Ports.First > document.Machine.Ports.Last {
		return errors.New("machine port range is invalid")
	}
	if document.Machine.Execution.ShellDefault != "deny" {
		return errors.New("machine execution shellDefault must be deny")
	}
	if len(document.Repositories) == 0 {
		return errors.New("at least one repository is required")
	}
	seenRoots := make(map[string]string, len(document.Repositories))
	for key, repository := range document.Repositories {
		if !identifierPattern.MatchString(key) {
			return fmt.Errorf("repository key %q is invalid", key)
		}
		if strings.TrimSpace(repository.DisplayName) == "" || len(repository.DisplayName) > 120 {
			return fmt.Errorf("repository %q displayName is invalid", key)
		}
		for field, path := range map[string]string{
			"root": repository.Root, "git.managedWorktreesRoot": repository.Git.ManagedWorktreesRoot,
		} {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
				return fmt.Errorf("repository %q %s must be a clean absolute path", key, field)
			}
		}
		if other, exists := seenRoots[repository.Root]; exists {
			return fmt.Errorf("repositories %q and %q use the same root", other, key)
		}
		seenRoots[repository.Root] = key
		if strings.TrimSpace(repository.Git.Remote) == "" || strings.TrimSpace(repository.Git.DefaultBase) == "" {
			return fmt.Errorf("repository %q git remote and defaultBase are required", key)
		}
		if repository.DefaultTarget != "" {
			if _, exists := repository.Targets[repository.DefaultTarget]; !exists {
				return fmt.Errorf("repository %q defaultTarget %q is not defined", key, repository.DefaultTarget)
			}
		}
		if err := validatePreparation(key, repository.Preparation); err != nil {
			return err
		}
		if err := validateRepositoryRuntime(key, repository); err != nil {
			return err
		}
	}
	return nil
}

func validateRepositoryRuntime(repositoryKey string, repository Repository) error {
	for id, target := range repository.Targets {
		risk := target.Risk
		if risk == "" {
			risk = "local"
		}
		if !identifierPattern.MatchString(id) ||
			(risk != "local" && risk != "remote-read" && risk != "remote-write") ||
			validateValueEnvironment(target.Environment, "target") != nil {
			return fmt.Errorf("repository %q target %q is invalid", repositoryKey, id)
		}
		if risk == "remote-write" && !target.WarnOnStart {
			return fmt.Errorf("repository %q remote-write target %q must warn on start", repositoryKey, id)
		}
	}
	for id, value := range repository.Values {
		if !identifierPattern.MatchString(id) || value.Kind == "" ||
			(value.Kind != "text-file" && value.Kind != "dotenv" && value.Kind != "json-pointer" && value.Kind != "yaml-scalar") ||
			(value.Root != "repository" && value.Root != "worktree") || !safeRelativePath(value.Path) {
			return fmt.Errorf("repository %q value %q is invalid", repositoryKey, id)
		}
	}
	for id, cache := range repository.Caches {
		if !identifierPattern.MatchString(id) || (cache.Directory != "" && !safeRelativePath(cache.Directory)) {
			return fmt.Errorf("repository %q cache %q is invalid", repositoryKey, id)
		}
	}
	for id, toolchain := range repository.Toolchains {
		if !identifierPattern.MatchString(id) || strings.TrimSpace(toolchain.RequestedVersion) == "" ||
			(toolchain.Executable == "" && toolchain.Provision == nil) {
			return fmt.Errorf("repository %q toolchain %q is invalid", repositoryKey, id)
		}
		if toolchain.Executable != "" && !cleanAbsolutePath(toolchain.Executable) {
			return fmt.Errorf("repository %q toolchain %q executable is invalid", repositoryKey, id)
		}
		if toolchain.Provision != nil && validateCommand(*toolchain.Provision, repository, "toolchain") != nil {
			return fmt.Errorf("repository %q toolchain %q provision command is invalid", repositoryKey, id)
		}
	}
	for id, artifact := range repository.Artifacts {
		if !identifierPattern.MatchString(id) || len(artifact.Content) > 256*1024 || strings.ContainsRune(artifact.Content, 0) ||
			((artifact.Content == "") == (artifact.Segments == nil)) || len(artifact.Segments) > 4096 ||
			(artifact.Filename != "" && (!safeRelativePath(artifact.Filename) || filepath.Base(artifact.Filename) != artifact.Filename)) {
			return fmt.Errorf("repository %q artifact %q is invalid", repositoryKey, id)
		}
		for _, segment := range artifact.Segments {
			if validateValueRef(segment, repository) != nil {
				return fmt.Errorf("repository %q artifact %q segment is invalid", repositoryKey, id)
			}
		}
	}
	for id, infrastructure := range repository.Infrastructure {
		if !identifierPattern.MatchString(id) || infrastructure.Kind != "container" ||
			strings.TrimSpace(infrastructure.Image) == "" || strings.ContainsAny(infrastructure.Image, "\x00\r\n") {
			return fmt.Errorf("repository %q infrastructure %q is invalid", repositoryKey, id)
		}
		for bindingID, binding := range infrastructure.ContainerPorts {
			service, found := repository.Services[binding.Service]
			_, portFound := service.Ports[binding.Purpose]
			if !identifierPattern.MatchString(bindingID) || !found || !portFound || binding.ContainerPort < 1 || binding.ContainerPort > 65535 {
				return fmt.Errorf("repository %q infrastructure %q port is invalid", repositoryKey, id)
			}
		}
	}
	for id, service := range repository.Services {
		if err := validateService(repositoryKey, id, service, repository); err != nil {
			return err
		}
	}
	if err := validateServiceGraph(repository.Services); err != nil {
		return fmt.Errorf("repository %q service dependencies are invalid: %w", repositoryKey, err)
	}
	for id, action := range repository.Actions {
		if !identifierPattern.MatchString(id) || strings.TrimSpace(action.DisplayName) == "" ||
			(action.Scope != "machine" && action.Scope != "repository" && action.Scope != "worktree" && action.Scope != "environment" && action.Scope != "service") ||
			(action.Risk != "local" && action.Risk != "remote-read" && action.Risk != "remote-write") ||
			((action.Command == nil) == (action.Lifecycle == "")) {
			return fmt.Errorf("repository %q action %q is invalid", repositoryKey, id)
		}
		if action.Command != nil && validateCommand(*action.Command, repository, "action") != nil {
			return fmt.Errorf("repository %q action %q command is invalid", repositoryKey, id)
		}
		switch action.Lifecycle {
		case "", "prepare", "start", "stop", "cleanup":
		default:
			return fmt.Errorf("repository %q action %q lifecycle is invalid", repositoryKey, id)
		}
	}
	if repository.Cleanup.PreparationRetention < 0 || repository.Cleanup.PreparationRetention > 100 {
		return fmt.Errorf("repository %q cleanup retention is invalid", repositoryKey)
	}
	return nil
}

func validateService(repositoryKey, id string, service Service, repository Repository) error {
	if !identifierPattern.MatchString(id) || strings.TrimSpace(service.DisplayName) == "" || strings.TrimSpace(service.Kind) == "" {
		return fmt.Errorf("repository %q service %q is invalid", repositoryKey, id)
	}
	if !service.IsAvailable() {
		if strings.TrimSpace(service.UnavailableReason) == "" {
			return fmt.Errorf("repository %q unavailable service %q needs a reason", repositoryKey, id)
		}
		return nil
	}
	if service.Command.Executable == "" || validateCommand(service.Command, repository, "service") != nil ||
		validateValueEnvironment(service.Environment, "service") != nil {
		return fmt.Errorf("repository %q service %q command is invalid", repositoryKey, id)
	}
	seenDependencies := make(map[string]struct{}, len(service.Dependencies))
	for _, dependency := range service.Dependencies {
		if dependency == id || repository.Services[dependency].DisplayName == "" {
			return fmt.Errorf("repository %q service %q dependency is invalid", repositoryKey, id)
		}
		if _, duplicate := seenDependencies[dependency]; duplicate {
			return fmt.Errorf("repository %q service %q dependency is duplicated", repositoryKey, id)
		}
		seenDependencies[dependency] = struct{}{}
	}
	for purpose, port := range service.Ports {
		if !identifierPattern.MatchString(purpose) || len(port.Preferred) > 16 {
			return fmt.Errorf("repository %q service %q port is invalid", repositoryKey, id)
		}
		seenPorts := make(map[int]struct{}, len(port.Preferred))
		for _, preferred := range port.Preferred {
			if preferred < 1024 || preferred > 65535 {
				return fmt.Errorf("repository %q service %q preferred port is invalid", repositoryKey, id)
			}
			if _, duplicate := seenPorts[preferred]; duplicate {
				return fmt.Errorf("repository %q service %q preferred port is duplicated", repositoryKey, id)
			}
			seenPorts[preferred] = struct{}{}
		}
		seenPublished := make(map[string]struct{}, len(port.Publish))
		for _, published := range port.Publish {
			if strings.TrimSpace(published.Name) == "" ||
				(published.Scheme != "http" && published.Scheme != "https") ||
				(published.Host != "loopback" && published.Host != "localhost") ||
				(published.Path != "" && !strings.HasPrefix(published.Path, "/")) {
				return fmt.Errorf("repository %q service %q published URL is invalid", repositoryKey, id)
			}
			if _, duplicate := seenPublished[published.Name]; duplicate {
				return fmt.Errorf("repository %q service %q published URL is duplicated", repositoryKey, id)
			}
			seenPublished[published.Name] = struct{}{}
		}
	}
	for _, command := range service.Prepare {
		if validateCommand(command, repository, "preparation") != nil {
			return fmt.Errorf("repository %q service %q preparation is invalid", repositoryKey, id)
		}
	}
	for _, command := range service.Initialize {
		if validateCommand(command, repository, "initialization") != nil {
			return fmt.Errorf("repository %q service %q initialization is invalid", repositoryKey, id)
		}
	}
	for _, probe := range append(append([]Probe{}, service.Readiness...), service.Health...) {
		_, portFound := service.Ports[probe.Port]
		if (probe.Kind != "tcp" && probe.Kind != "http") || !portFound ||
			(probe.Kind == "http" && probe.Path != "" && !strings.HasPrefix(probe.Path, "/")) {
			return fmt.Errorf("repository %q service %q probe is invalid", repositoryKey, id)
		}
		for _, accepted := range probe.AcceptedStatuses {
			if accepted.Minimum < 100 || accepted.Maximum > 599 || accepted.Minimum > accepted.Maximum {
				return fmt.Errorf("repository %q service %q probe status is invalid", repositoryKey, id)
			}
		}
	}
	for _, infrastructure := range service.Infrastructure {
		if _, found := repository.Infrastructure[infrastructure]; !found {
			return fmt.Errorf("repository %q service %q infrastructure is unknown", repositoryKey, id)
		}
	}
	for _, artifact := range service.Artifacts {
		if _, found := repository.Artifacts[artifact]; !found {
			return fmt.Errorf("repository %q service %q artifact is unknown", repositoryKey, id)
		}
	}
	return nil
}

func validateCommand(command Command, repository Repository, kind string) error {
	if !cleanAbsolutePath(command.Executable) || command.WorkingDirectory == "" || !safeRelativePath(command.WorkingDirectory) {
		return errors.New("command paths are invalid")
	}
	duration, err := time.ParseDuration(command.Timeout)
	if err != nil || duration <= 0 || duration > 30*time.Minute {
		return errors.New("command timeout is invalid")
	}
	if len(command.Arguments) > 1024 || validateValueEnvironment(command.Environment, kind) != nil {
		return errors.New("command arguments or environment are invalid")
	}
	for _, argument := range command.Arguments {
		if validateValueRef(argument, repository) != nil {
			return errors.New("command argument is invalid")
		}
	}
	for _, value := range command.Environment {
		if validateValueRef(value, repository) != nil {
			return errors.New("command environment is invalid")
		}
	}
	return nil
}

func validateValueEnvironment(environment map[string]ValueRef, _ string) error {
	for name := range environment {
		if !environmentNamePattern.MatchString(name) || name == "HOME" || name == "PATH" || name == "TMPDIR" {
			return errors.New("environment name is invalid")
		}
	}
	return nil
}

func validateValueRef(value ValueRef, repository Repository) error {
	count := 0
	if value.Literal != nil {
		count++
		if strings.ContainsRune(*value.Literal, 0) {
			return errors.New("literal contains NUL")
		}
	}
	for _, reference := range []string{value.Target, value.Artifact, value.Cache, value.Value} {
		if reference != "" {
			count++
		}
	}
	if value.Port != nil {
		count++
		if !identifierPattern.MatchString(value.Port.Purpose) ||
			(value.Port.Service != "" && !identifierPattern.MatchString(value.Port.Service)) {
			return errors.New("port reference is invalid")
		}
	}
	if value.URL != nil {
		count++
		if !identifierPattern.MatchString(value.URL.Purpose) ||
			(value.URL.Service != "" && !identifierPattern.MatchString(value.URL.Service)) ||
			(value.URL.Scheme != "http" && value.URL.Scheme != "https") ||
			(value.URL.Host != "loopback" && value.URL.Host != "localhost") ||
			(value.URL.Path != "" && !strings.HasPrefix(value.URL.Path, "/")) {
			return errors.New("URL reference is invalid")
		}
	}
	for _, path := range []*string{value.WorktreePath, value.RuntimePath} {
		if path != nil {
			count++
			if !safeRelativePath(*path) {
				return errors.New("path reference is invalid")
			}
		}
	}
	if count != 1 {
		return errors.New("value reference must select exactly one source")
	}
	if value.Artifact != "" {
		if _, found := repository.Artifacts[value.Artifact]; !found {
			return errors.New("artifact reference is unknown")
		}
	}
	if value.Cache != "" {
		if _, found := repository.Caches[value.Cache]; !found {
			return errors.New("cache reference is unknown")
		}
	}
	if value.Value != "" {
		if _, found := repository.Values[value.Value]; !found {
			return errors.New("value reference is unknown")
		}
	}
	return nil
}

func validateServiceGraph(services map[string]Service) error {
	visiting := make(map[string]bool, len(services))
	visited := make(map[string]bool, len(services))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return errors.New("dependency cycle")
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range services[id].Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range services {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator) && !strings.ContainsRune(path, 0)
}

func safeRelativePath(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != ".." &&
		!strings.HasPrefix(path, ".."+string(filepath.Separator)) && !strings.ContainsRune(path, 0)
}

func validatePreparation(repositoryKey string, preparation Preparation) error {
	if len(preparation.Steps) == 0 && len(preparation.Verify) == 0 &&
		len(preparation.Fingerprint.Files) == 0 && len(preparation.Fingerprint.Globs) == 0 {
		return nil
	}
	if len(preparation.Steps) == 0 || len(preparation.Verify) == 0 ||
		len(preparation.Fingerprint.Files)+len(preparation.Fingerprint.Globs) == 0 {
		return fmt.Errorf("repository %q preparation is incomplete", repositoryKey)
	}
	seen := make(map[string]struct{}, len(preparation.Steps))
	for _, step := range preparation.Steps {
		if !identifierPattern.MatchString(step.ID) || !filepath.IsAbs(step.Executable) ||
			filepath.Clean(step.Executable) != step.Executable || step.WorkingDirectory == "" ||
			filepath.IsAbs(step.WorkingDirectory) || filepath.Clean(step.WorkingDirectory) != step.WorkingDirectory {
			return fmt.Errorf("repository %q preparation step is invalid", repositoryKey)
		}
		if _, duplicate := seen[step.ID]; duplicate {
			return fmt.Errorf("repository %q preparation step %q is duplicated", repositoryKey, step.ID)
		}
		seen[step.ID] = struct{}{}
		timeout, err := time.ParseDuration(step.Timeout)
		if err != nil || timeout <= 0 || timeout > 30*time.Minute {
			return fmt.Errorf("repository %q preparation step timeout is invalid", repositoryKey)
		}
		for name, value := range step.Environment {
			if !environmentNamePattern.MatchString(name) || strings.ContainsRune(value, 0) {
				return fmt.Errorf("repository %q preparation environment is invalid", repositoryKey)
			}
			if name == "HOME" || name == "PATH" || name == "TMPDIR" {
				return fmt.Errorf("repository %q preparation environment overrides a trusted base value", repositoryKey)
			}
		}
	}
	for _, verification := range preparation.Verify {
		if !identifierPattern.MatchString(verification.ID) ||
			(verification.Kind != "directory" && verification.Kind != "regular-file" && verification.Kind != "executable") ||
			verification.Path == "" || filepath.IsAbs(verification.Path) || filepath.Clean(verification.Path) != verification.Path {
			return fmt.Errorf("repository %q preparation verification is invalid", repositoryKey)
		}
	}
	for _, path := range preparation.Fingerprint.Files {
		if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("repository %q preparation fingerprint file is invalid", repositoryKey)
		}
	}
	for _, pattern := range preparation.Fingerprint.Globs {
		if pattern == "" || filepath.IsAbs(pattern) || filepath.Clean(pattern) != pattern || strings.Contains(pattern, "..") {
			return fmt.Errorf("repository %q preparation fingerprint glob is invalid", repositoryKey)
		}
	}
	return nil
}

func sortedRepositoryKeys(repositories map[string]Repository) []string {
	keys := make([]string, 0, len(repositories))
	for key := range repositories {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func digest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(sum[:])
}

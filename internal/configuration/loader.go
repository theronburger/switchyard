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

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const (
	maximumConfigurationBytes = 2 << 20
	maximumYAMLDepth          = 32
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

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
	repositoryDigests := make(map[string]string, len(document.Repositories))
	for _, key := range sortedRepositoryKeys(document.Repositories) {
		repositoryPayload, err := json.Marshal(document.Repositories[key])
		if err != nil {
			return Loaded{}, fmt.Errorf("canonicalize repository %q: %w", key, err)
		}
		repositoryDigests[key] = digest(repositoryPayload)
	}
	return Loaded{
		Document: document, CanonicalPayload: payload, Digest: digest(payload),
		SourceDigest: digest(contents), RepositoryDigests: repositoryDigests,
	}, nil
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

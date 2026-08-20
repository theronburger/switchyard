package configuration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfiguration = `schemaVersion: 1
machine:
  ports:
    first: 30000
    last: 49999
  execution:
    inheritedEnvironment: []
    shellDefault: deny
secretProviders:
  login-keychain:
    kind: macos-keychain
repositories:
  sample-one:
    enabled: true
    displayName: Sample One
    root: /tmp/sample-one
    git:
      remote: origin
      defaultBase: origin/main
      managedWorktreesRoot: /tmp/sample-one-worktrees
    values: {}
    toolchains: {}
    caches: {}
    environmentSources: {}
    preparation: {}
    targets:
      local: {}
    defaultTarget: local
    services: {}
    infrastructure: {}
    artifacts: {}
    actions: {}
    cleanup: {}
`

func TestParseProducesStableCanonicalDigests(t *testing.T) {
	first, err := Parse([]byte(validConfiguration))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse([]byte(strings.Replace(validConfiguration, "first: 30000", "first: 30000 # comment", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("canonical digest changed with YAML presentation: %q != %q", first.Digest, second.Digest)
	}
	if first.SourceDigest == second.SourceDigest {
		t.Fatal("source digest did not identify the edited desired file")
	}
	if first.RepositoryDigests["sample-one"] == "" {
		t.Fatal("repository digest is empty")
	}
}

func TestParseRejectsUnsafeYAMLAndUnknownFields(t *testing.T) {
	tests := map[string]string{
		"duplicate":       strings.Replace(validConfiguration, "schemaVersion: 1", "schemaVersion: 1\nschemaVersion: 1", 1),
		"anchor":          strings.Replace(validConfiguration, "ports:", "ports: &ports", 1),
		"alias":           strings.Replace(validConfiguration, "ports:\n", "ports: &ports\n", 1) + "other: *ports\n",
		"merge":           strings.Replace(validConfiguration, "enabled: true", "<<: {enabled: true}", 1),
		"tag":             strings.Replace(validConfiguration, "displayName: Sample One", "displayName: !private Sample One", 1),
		"unknown":         strings.Replace(validConfiguration, "schemaVersion: 1", "schemaVersion: 1\nunknown: true", 1),
		"second-document": validConfiguration + "---\n{}\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(contents)); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func TestLoadFileRequiresPrivateRegularFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "configuration.yaml")
	if err := os.WriteFile(path, []byte(validConfiguration), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("world-readable configuration was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(link); err == nil {
		t.Fatal("configuration symlink was accepted")
	}
}

func TestParseRejectsInvalidRepositoryReferences(t *testing.T) {
	invalid := strings.Replace(validConfiguration, "defaultTarget: local", "defaultTarget: missing", 1)
	if _, err := Parse([]byte(invalid)); err == nil {
		t.Fatal("missing default target was accepted")
	}
}

func TestParseAcceptsGenericPreparationWithoutRepositoryCode(t *testing.T) {
	configured := strings.Replace(validConfiguration, "preparation: {}", `preparation:
      fingerprint:
        files: [lockfile]
        globs: []
      steps:
        - id: install
          executable: /usr/bin/true
          arguments: [--version]
          workingDirectory: .
          environment:
            CACHE_MODE: shared
          timeout: 30s
      verify:
        - id: dependencies
          kind: directory
          path: dependencies`, 1)
	loaded, err := Parse([]byte(configured))
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Document.Repositories["sample-one"].Preparation.Steps[0].Executable; got != "/usr/bin/true" {
		t.Fatalf("executable: got %q", got)
	}
}

func TestParseRejectsTrustedEnvironmentOverride(t *testing.T) {
	configured := strings.Replace(validConfiguration, "preparation: {}", `preparation:
      fingerprint: {files: [lockfile], globs: []}
      steps:
        - id: install
          executable: /usr/bin/true
          arguments: [--version]
          workingDirectory: .
          environment: {HOME: /tmp/hostile}
          timeout: 30s
      verify:
        - {id: dependencies, kind: directory, path: dependencies}`, 1)
	if _, err := Parse([]byte(configured)); err == nil {
		t.Fatal("trusted environment override was accepted")
	}
}

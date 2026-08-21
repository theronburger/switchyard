package configuration

import (
	"os"
	"path/filepath"
	"strconv"
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
  key-session:
    kind: key-session
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

func TestParseAllowsExplicitHostHomeReferenceOnly(t *testing.T) {
	configured := strings.Replace(validConfiguration, "local: {}", `local:
        environment:
          HOME: {hostHome: true}`, 1)
	if _, err := Parse([]byte(configured)); err != nil {
		t.Fatal(err)
	}
	literal := strings.Replace(validConfiguration, "local: {}", `local:
        environment:
          HOME: {literal: /tmp/hostile}`, 1)
	if _, err := Parse([]byte(literal)); err == nil {
		t.Fatal("literal HOME override was accepted")
	}
}

func TestParseRejectsUnknownNestedServiceField(t *testing.T) {
	configured := strings.Replace(validConfiguration, "services: {}", `services:
      web:
        displayName: Web
        kind: web
        mystery: true`, 1)
	if _, err := Parse([]byte(configured)); err == nil {
		t.Fatal("unknown nested service field was accepted")
	}
}

func TestParseValidatesProfileActions(t *testing.T) {
	withActions := func(actions string) string {
		return strings.Replace(validConfiguration, "    actions: {}\n", "    actions:\n"+actions, 1)
	}
	accepted := []string{
		"      tidy:\n        displayName: Tidy\n        scope: worktree\n        risk: local\n        command:\n          executable: /bin/echo\n          arguments: [{literal: tidy}]\n          workingDirectory: .\n          environment: {}\n          timeout: 1m\n",
		"      warm:\n        displayName: Prepare\n        scope: worktree\n        risk: local\n        lifecycle: prepare\n",
		"      halt:\n        displayName: Stop\n        scope: environment\n        risk: local\n        lifecycle: stop\n",
		"      sweep:\n        displayName: Cleanup\n        scope: repository\n        risk: local\n        lifecycle: cleanup\n",
	}
	for _, actions := range accepted {
		if _, err := Parse([]byte(withActions(actions))); err != nil {
			t.Fatalf("accepted action rejected: %v\n%s", err, actions)
		}
	}
	rejected := []string{
		// lifecycle stop must address an environment
		"      halt:\n        displayName: Stop\n        scope: worktree\n        risk: local\n        lifecycle: stop\n",
		// lifecycle prepare cannot address a service
		"      warm:\n        displayName: Prepare\n        scope: service\n        risk: local\n        lifecycle: prepare\n",
		// both command and lifecycle
		"      both:\n        displayName: Both\n        scope: worktree\n        risk: local\n        lifecycle: prepare\n        command:\n          executable: /bin/echo\n          arguments: []\n          workingDirectory: .\n          environment: {}\n          timeout: 1m\n",
		// relative executable is never a shell lookup
		"      rel:\n        displayName: Rel\n        scope: worktree\n        risk: local\n        command:\n          executable: echo\n          arguments: []\n          workingDirectory: .\n          environment: {}\n          timeout: 1m\n",
		// unknown risk
		"      risky:\n        displayName: Risky\n        scope: worktree\n        risk: destructive\n        command:\n          executable: /bin/echo\n          arguments: []\n          workingDirectory: .\n          environment: {}\n          timeout: 1m\n",
		// timeout beyond the bound
		"      slow:\n        displayName: Slow\n        scope: worktree\n        risk: local\n        command:\n          executable: /bin/echo\n          arguments: []\n          workingDirectory: .\n          environment: {}\n          timeout: 31m\n",
	}
	for _, actions := range rejected {
		if _, err := Parse([]byte(withActions(actions))); err == nil {
			t.Fatalf("invalid action accepted:\n%s", actions)
		}
	}
}

func TestParseValidatesEnvironmentSources(t *testing.T) {
	withSources := func(sources string) string {
		return strings.Replace(strings.Replace(validConfiguration, "environmentSources: {}", "environmentSources:\n"+sources, 1),
			"local: {}", "local: {}\n      staging: {}", 1)
	}
	valid := withSources(`      base:
        kind: dotenv
        root: worktree
        path: .env
        optional: true
        allow: [APP_NAME, LOG_LEVEL]
      staging:
        kind: dotenv
        root: repository
        path: config/staging.env
        targets: [staging]
        allow: [LOG_FORMAT]`)
	loaded, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	source := loaded.Document.Repositories["sample-one"].EnvironmentSources["base"]
	if !source.Optional || len(source.Allow) != 2 || len(source.Targets) != 0 {
		t.Fatalf("parsed source: %+v", source)
	}
	rejected := map[string]string{
		"shell kind":     `      base: {kind: shell, root: worktree, path: .env, allow: [APP_NAME]}`,
		"bad root":       `      base: {kind: dotenv, root: runtime, path: .env, allow: [APP_NAME]}`,
		"absolute":       `      base: {kind: dotenv, root: worktree, path: /etc/environment, allow: [APP_NAME]}`,
		"escape":         `      base: {kind: dotenv, root: worktree, path: ../other/.env, allow: [APP_NAME]}`,
		"dot path":       `      base: {kind: dotenv, root: worktree, path: ., allow: [APP_NAME]}`,
		"empty allow":    `      base: {kind: dotenv, root: worktree, path: .env, allow: []}`,
		"bad ID":         `      Base: {kind: dotenv, root: worktree, path: .env, allow: [APP_NAME]}`,
		"PATH":           `      base: {kind: dotenv, root: worktree, path: .env, allow: [PATH]}`,
		"HOME":           `      base: {kind: dotenv, root: worktree, path: .env, allow: [HOME]}`,
		"TMPDIR":         `      base: {kind: dotenv, root: worktree, path: .env, allow: [TMPDIR]}`,
		"DYLD":           `      base: {kind: dotenv, root: worktree, path: .env, allow: [DYLD_INSERT_LIBRARIES]}`,
		"LD":             `      base: {kind: dotenv, root: worktree, path: .env, allow: [LD_PRELOAD]}`,
		"BASH_ENV":       `      base: {kind: dotenv, root: worktree, path: .env, allow: [BASH_ENV]}`,
		"SWITCHYARD_":    `      base: {kind: dotenv, root: worktree, path: .env, allow: [SWITCHYARD_TOKEN]}`,
		"bad name":       `      base: {kind: dotenv, root: worktree, path: .env, allow: [APP-NAME]}`,
		"dup name":       `      base: {kind: dotenv, root: worktree, path: .env, allow: [APP_NAME, APP_NAME]}`,
		"unknown target": `      base: {kind: dotenv, root: worktree, path: .env, targets: [production], allow: [APP_NAME]}`,
		"dup target":     `      base: {kind: dotenv, root: worktree, path: .env, targets: [local, local], allow: [APP_NAME]}`,
		"ambiguous everywhere": `      a: {kind: dotenv, root: worktree, path: a.env, allow: [APP_NAME]}
      b: {kind: dotenv, root: worktree, path: b.env, allow: [APP_NAME]}`,
		"ambiguous on one target": `      a: {kind: dotenv, root: worktree, path: a.env, allow: [APP_NAME]}
      b: {kind: dotenv, root: worktree, path: b.env, targets: [staging], allow: [APP_NAME]}`,
		"unknown field": `      base: {kind: dotenv, root: worktree, path: .env, allow: [APP_NAME], key: APP_NAME}`,
	}
	for name, sources := range rejected {
		if _, err := Parse([]byte(withSources(sources))); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	disjoint := withSources(`      a: {kind: dotenv, root: worktree, path: a.env, targets: [local], allow: [APP_NAME]}
      b: {kind: dotenv, root: worktree, path: b.env, targets: [staging], allow: [APP_NAME]}`)
	if _, err := Parse([]byte(disjoint)); err != nil {
		t.Fatalf("disjoint targets were rejected: %v", err)
	}
	var many strings.Builder
	for index := 0; index <= 32; index++ {
		many.WriteString("      s" + strconv.Itoa(index) + ": {kind: dotenv, root: worktree, path: .env, allow: [N" + strconv.Itoa(index) + "]}\n")
	}
	if _, err := Parse([]byte(withSources(many.String()))); err == nil {
		t.Fatal("source count bound was not enforced")
	}
}

// TestParseSecretProvidersFailClosed proves the accepted schema carries only
// non-secret provider references: the sole accepted kind is key-session, any
// other kind or malformed key is rejected, and no value reference can name a
// secret, provider, profile, lease, or consumer capability. The canonical
// payload of an accepted document therefore never contains a secret value.
func TestParseSecretProvidersFailClosed(t *testing.T) {
	rejected := map[string]string{
		"other-kind":  strings.Replace(validConfiguration, "kind: key-session", "kind: macos-keychain", 1),
		"empty-kind":  strings.Replace(validConfiguration, "kind: key-session", "kind: \"\"", 1),
		"bad-key":     strings.Replace(validConfiguration, "  key-session:\n    kind: key-session", "  Login Keychain:\n    kind: key-session", 1),
		"secret-data": strings.Replace(validConfiguration, "kind: key-session", "kind: key-session\n    secret: hunter2", 1),
		"secret-value-ref": strings.Replace(validConfiguration, "targets:\n      local: {}",
			"targets:\n      local:\n        environment:\n          API_TOKEN: { secret: { provider: key-session, profile: sample } }", 1),
		"lease-value-ref": strings.Replace(validConfiguration, "targets:\n      local: {}",
			"targets:\n      local:\n        environment:\n          API_TOKEN: { lease: lease_example }", 1),
	}
	for name, contents := range rejected {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(contents)); err == nil {
				t.Fatal("secret-bearing or unknown secret provider configuration was accepted")
			}
		})
	}
	loaded, err := Parse([]byte(validConfiguration))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Document.SecretProviders["key-session"].Kind != SecretProviderKindKeySession {
		t.Fatalf("accepted provider kind = %q", loaded.Document.SecretProviders["key-session"].Kind)
	}
	if strings.Contains(string(loaded.CanonicalPayload), "hunter2") || strings.Contains(string(loaded.CanonicalPayload), "lease_") {
		t.Fatal("canonical payload carries a secret or lease value")
	}
}

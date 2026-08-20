package configuration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var sampleEntry = RepositoryEntry{
	Key: "sample-two", Enabled: true, DisplayName: "Sample Two: \"quoted\"", Root: "/tmp/sample-two",
	Remote: "origin", DefaultBase: "origin/main", ManagedWorktreesRoot: "/tmp/sample-two-worktrees",
}

const commentedConfiguration = `schemaVersion: 1
# machine-wide defaults
machine:
  ports:
    first: 30000
    last: 49999
  execution:
    inheritedEnvironment: []
    shellDefault: deny
secretProviders: {}
repositories:
  sample-one:
    enabled: true
    displayName: Sample One # shown in the sidebar
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
      local: {displayName: Local}
    defaultTarget: local
    services: {}
    infrastructure: {}
    artifacts: {}
    actions: {}
    cleanup: {preparationRetention: 3}
`

func TestUpsertRepositoryAddsAnEntryWithoutDisturbingTheRest(t *testing.T) {
	edited, err := UpsertRepository([]byte(commentedConfiguration), sampleEntry)
	if err != nil {
		t.Fatal(err)
	}
	text := string(edited)
	for _, expected := range []string{"# machine-wide defaults", "# shown in the sidebar", "preparationRetention: 3", "local: {displayName: Local}"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("edit lost %q:\n%s", expected, text)
		}
	}
	loaded, err := Parse(edited)
	if err != nil {
		t.Fatalf("edited configuration does not compile: %v\n%s", err, text)
	}
	added := loaded.Document.Repositories["sample-two"]
	if added.DisplayName != sampleEntry.DisplayName || added.Root != sampleEntry.Root ||
		added.Git.ManagedWorktreesRoot != sampleEntry.ManagedWorktreesRoot || !added.Enabled {
		t.Fatalf("added entry round-tripped incorrectly: %+v", added)
	}
	if loaded.Document.Repositories["sample-one"].Cleanup.PreparationRetention != 3 {
		t.Fatal("existing entry changed")
	}
}

func TestUpsertRepositoryUpdatesOnlyGenericFieldsInPlace(t *testing.T) {
	update := RepositoryEntry{
		Key: "sample-one", Enabled: false, DisplayName: "Renamed", Root: "/tmp/sample-one",
		Remote: "upstream", DefaultBase: "upstream/trunk", ManagedWorktreesRoot: "/tmp/elsewhere",
	}
	edited, err := UpsertRepository([]byte(commentedConfiguration), update)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Parse(edited)
	if err != nil {
		t.Fatalf("%v\n%s", err, edited)
	}
	repository := loaded.Document.Repositories["sample-one"]
	if repository.Enabled || repository.DisplayName != "Renamed" || repository.Git.Remote != "upstream" ||
		repository.Git.DefaultBase != "upstream/trunk" || repository.Git.ManagedWorktreesRoot != "/tmp/elsewhere" {
		t.Fatalf("generic fields were not updated: %+v", repository)
	}
	if repository.DefaultTarget != "local" || repository.Cleanup.PreparationRetention != 3 {
		t.Fatalf("non-generic sections were disturbed: %+v", repository)
	}
	if !strings.Contains(string(edited), "# shown in the sidebar") {
		t.Fatalf("line comment on an edited scalar was lost:\n%s", edited)
	}
}

func TestUpsertRepositoryRefusesToRepointAnExistingKey(t *testing.T) {
	repointed := sampleEntry
	repointed.Key = "sample-one"
	repointed.Root = "/tmp/somewhere-else"
	if _, err := UpsertRepository([]byte(commentedConfiguration), repointed); !errors.Is(err, ErrRepositoryRootBound) {
		t.Fatalf("expected root binding error, got %v", err)
	}
}

func TestRemoveRepositoryRemovesExactlyOneEntry(t *testing.T) {
	withTwo, err := UpsertRepository([]byte(commentedConfiguration), sampleEntry)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveRepository(withTwo, "sample-one")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Parse(removed)
	if err != nil {
		t.Fatalf("%v\n%s", err, removed)
	}
	if _, still := loaded.Document.Repositories["sample-one"]; still || len(loaded.Document.Repositories) != 1 {
		t.Fatalf("unexpected repositories after removal: %+v", loaded.Document.Repositories)
	}
	if _, err := RemoveRepository(removed, "sample-one"); !errors.Is(err, ErrRepositoryMissing) {
		t.Fatalf("expected missing repository error, got %v", err)
	}
}

func TestEditorRejectsMalformedAndHostileYAML(t *testing.T) {
	cases := map[string]string{
		"anchor":        strings.Replace(commentedConfiguration, "remote: origin", "remote: &r origin", 1),
		"merge key":     strings.Replace(commentedConfiguration, "    values: {}", "    <<: {}\n    values: {}", 1),
		"custom tag":    strings.Replace(commentedConfiguration, "root: /tmp/sample-one", "root: !!binary /tmp", 1),
		"duplicate key": strings.Replace(commentedConfiguration, "    values: {}", "    values: {}\n    values: {}", 1),
		"two documents": commentedConfiguration + "---\nschemaVersion: 1\n",
		"truncated":     commentedConfiguration[:len(commentedConfiguration)-40] + "\n  [",
		"scalar root":   "just a string\n",
		"binary":        "schemaVersion: 1\x00",
	}
	for name, contents := range cases {
		if _, err := UpsertRepository([]byte(contents), sampleEntry); err == nil {
			t.Errorf("%s: expected upsert rejection", name)
		}
		if _, err := RemoveRepository([]byte(contents), "sample-one"); err == nil {
			t.Errorf("%s: expected removal rejection", name)
		}
	}
}

func TestNewDocumentCompiles(t *testing.T) {
	contents, err := NewDocument(sampleEntry)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Parse(contents)
	if err != nil {
		t.Fatalf("%v\n%s", err, contents)
	}
	if loaded.Document.Machine.Ports.First != 30000 || loaded.Document.Machine.Execution.ShellDefault != "deny" ||
		len(loaded.Document.Repositories) != 1 {
		t.Fatalf("unexpected fresh document: %+v", loaded.Document)
	}
}

func privateDirectory(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "Switchyard")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestWriteDesiredCreatesAndReplacesAtomically(t *testing.T) {
	directory := privateDirectory(t)
	path := filepath.Join(directory, "configuration.yaml")
	if err := WriteDesired(path, []byte(commentedConfiguration), ""); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("written file is not a private regular file: %v %v", info, err)
	}
	first := ReadDesired(path)
	if !first.Present || first.Problem != nil || len(first.Entries()) != 1 {
		t.Fatalf("unexpected desired view: %+v", first)
	}
	if err := WriteDesired(path, []byte(commentedConfiguration), ""); !errors.Is(err, ErrDesiredChanged) {
		t.Fatalf("creating over an existing file must fail closed, got %v", err)
	}
	edited, err := UpsertRepository(first.Contents, sampleEntry)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDesired(path, edited, "sha256:0000000000000000000000000000000000000000000000000000000000000000"); !errors.Is(err, ErrDesiredChanged) {
		t.Fatalf("stale digest must fail closed, got %v", err)
	}
	if current, _ := os.ReadFile(path); string(current) != commentedConfiguration {
		t.Fatal("a refused write modified the desired file")
	}
	if err := WriteDesired(path, edited, first.SourceDigest); err != nil {
		t.Fatal(err)
	}
	if len(ReadDesired(path).Entries()) != 2 {
		t.Fatal("replacement did not land")
	}
	leftovers, _ := filepath.Glob(filepath.Join(directory, ".configuration.*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files were left behind: %v", leftovers)
	}
}

func TestWriteDesiredFailsClosedOnUnsafeTargets(t *testing.T) {
	directory := privateDirectory(t)
	path := filepath.Join(directory, "configuration.yaml")
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte(commentedConfiguration), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("symlink", func(t *testing.T) {
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(path) }()
		if err := WriteDesired(path, []byte(commentedConfiguration), digest([]byte(commentedConfiguration))); err == nil {
			t.Fatal("symlinked desired file must be refused")
		}
		if desired := ReadDesired(path); !desired.Present || desired.Problem == nil {
			t.Fatalf("symlink must surface as a problem: %+v", desired)
		}
		if current, _ := os.ReadFile(outside); string(current) != commentedConfiguration {
			t.Fatal("symlink target was modified")
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		if err := os.Link(outside, path); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(path) }()
		if err := WriteDesired(path, []byte(commentedConfiguration), digest([]byte(commentedConfiguration))); err == nil {
			t.Fatal("hard-linked desired file must be refused")
		}
		if ReadDesired(path).Problem == nil {
			t.Fatal("hard link must surface as a problem")
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		if err := os.WriteFile(path, []byte(commentedConfiguration), 0o644); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Remove(path) }()
		if err := WriteDesired(path, []byte(commentedConfiguration), digest([]byte(commentedConfiguration))); err == nil {
			t.Fatal("group-readable desired file must be refused")
		}
	})

	t.Run("directory mode", func(t *testing.T) {
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(directory, 0o700) }()
		if err := WriteDesired(path, []byte(commentedConfiguration), ""); err == nil {
			t.Fatal("shared configuration directory must be refused")
		}
	})

	t.Run("symlinked directory", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "linked")
		if err := os.Symlink(directory, link); err != nil {
			t.Fatal(err)
		}
		if err := WriteDesired(filepath.Join(link, "configuration.yaml"), []byte(commentedConfiguration), ""); err == nil {
			t.Fatal("symlinked configuration directory must be refused")
		}
	})

	t.Run("relative path", func(t *testing.T) {
		if err := WriteDesired("configuration.yaml", []byte(commentedConfiguration), ""); err == nil {
			t.Fatal("relative path must be refused")
		}
	})
}

func TestReadDesiredReportsMissingAndMalformedFiles(t *testing.T) {
	directory := privateDirectory(t)
	path := filepath.Join(directory, "configuration.yaml")
	if desired := ReadDesired(path); desired.Present || desired.Problem != nil {
		t.Fatalf("missing file must read as absent: %+v", desired)
	}
	if err := os.WriteFile(path, []byte("schemaVersion: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	desired := ReadDesired(path)
	if !desired.Present || desired.Problem == nil || desired.SourceDigest == "" || len(desired.Entries()) != 0 {
		t.Fatalf("malformed file must keep its digest and surface a problem: %+v", desired)
	}
}

package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theronburger/switchyard/internal/configuration"
)

func TestReadValuesResolvesBoundedSourcesWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runtime-version"), []byte("v24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := configuration.Repository{Values: map[string]configuration.ValueSource{
		"runtime-version": {Kind: "text-file", Root: "worktree", Path: "runtime-version", Trim: true, TrimPrefix: "v"},
	}}
	values, err := ReadValues(profile, root, root)
	if err != nil || values["runtime-version"] != "24" {
		t.Fatalf("values: %#v error=%v", values, err)
	}

	if err := os.Symlink(filepath.Join(root, "runtime-version"), filepath.Join(root, "version.link")); err != nil {
		t.Fatal(err)
	}
	profile.Values["runtime-version"] = configuration.ValueSource{Kind: "text-file", Root: "worktree", Path: "version.link"}
	if _, err := ReadValues(profile, root, root); err == nil {
		t.Fatal("symlinked value source was accepted")
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "version"), []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-directory")); err != nil {
		t.Fatal(err)
	}
	profile.Values["runtime-version"] = configuration.ValueSource{Kind: "text-file", Root: "worktree", Path: "linked-directory/version"}
	if _, err := ReadValues(profile, root, root); err == nil {
		t.Fatal("value source escaped through a symlinked parent directory")
	}
}

func TestReadValuesSupportsStructuredScalars(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"value.json": `{"nested":{"enabled":true}}`,
		"value.yaml": "nested:\n  count: 42\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile := configuration.Repository{Values: map[string]configuration.ValueSource{
		"enabled": {Kind: "json-pointer", Root: "repository", Path: "value.json", Key: "/nested/enabled"},
		"count":   {Kind: "yaml-scalar", Root: "repository", Path: "value.yaml", Key: "/nested/count"},
	}}
	values, err := ReadValues(profile, root, root)
	if err != nil || values["enabled"] != "true" || values["count"] != "42" {
		t.Fatalf("values: %#v error=%v", values, err)
	}
}

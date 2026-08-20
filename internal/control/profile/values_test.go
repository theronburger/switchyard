package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theronburger/switchyard/internal/configuration"
)

func TestReadValuesResolvesBoundedSourcesWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env.test"), []byte("TOKEN=\"local-test-value\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := configuration.Repository{Values: map[string]configuration.ValueSource{
		"token": {Kind: "dotenv", Root: "worktree", Path: ".env.test", Key: "TOKEN", Trim: true},
	}}
	values, err := ReadValues(profile, root, root)
	if err != nil || values["token"] != "local-test-value" {
		t.Fatalf("values: %#v error=%v", values, err)
	}

	if err := os.Symlink(filepath.Join(root, ".env.test"), filepath.Join(root, ".env.link")); err != nil {
		t.Fatal(err)
	}
	profile.Values["token"] = configuration.ValueSource{Kind: "dotenv", Root: "worktree", Path: ".env.link", Key: "TOKEN"}
	if _, err := ReadValues(profile, root, root); err == nil {
		t.Fatal("symlinked value source was accepted")
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.env"), []byte("TOKEN=outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-directory")); err != nil {
		t.Fatal(err)
	}
	profile.Values["token"] = configuration.ValueSource{Kind: "dotenv", Root: "worktree", Path: "linked-directory/secret.env", Key: "TOKEN"}
	if _, err := ReadValues(profile, root, root); err == nil {
		t.Fatal("value source escaped through a symlinked parent directory")
	}
}

func TestReadValuesSupportsStructuredScalarsAndRejectsDuplicateDotenvKeys(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"value.json":    `{"nested":{"enabled":true}}`,
		"value.yaml":    "nested:\n  count: 42\n",
		"duplicate.env": "TOKEN=one\nTOKEN=two\n",
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
	profile.Values = map[string]configuration.ValueSource{
		"token": {Kind: "dotenv", Root: "repository", Path: "duplicate.env", Key: "TOKEN"},
	}
	if _, err := ReadValues(profile, root, root); err == nil {
		t.Fatal("duplicate dotenv key was accepted")
	}
}

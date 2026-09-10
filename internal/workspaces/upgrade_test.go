package workspaces

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradePreservesLegacyAndNeverReplacesCurrentConfiguration(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "configuration.yaml")
	if err := os.WriteFile(legacy, []byte("private legacy configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeConfig(root, true); err == nil {
		t.Fatal("silently discarded legacy configuration")
	}
	candidate := filepath.Join(root, "upgrade", "config.json")
	config := Config{SchemaVersion: 1, Repositories: []RepositoryConfig{}, Choices: map[string]Choices{}}
	if err := SaveConfig(candidate, config); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeConfig(root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); !os.IsNotExist(err) {
		t.Fatal("check mutated configuration")
	}
	if err := UpgradeConfig(root, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeConfig(root, true); err != nil {
		t.Fatal("rerun must preserve existing valid config", err)
	}
	bytes, err := os.ReadFile(legacy)
	if err != nil || string(bytes) != "private legacy configuration" {
		t.Fatal("legacy backup changed")
	}
}

func TestInvalidUpgradeDoesNotCreateCurrentConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "upgrade"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "upgrade", "config.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeConfig(root, true); err == nil {
		t.Fatal("invalid recipe accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); !os.IsNotExist(err) {
		t.Fatal("invalid recipe installed")
	}
	if err := UpgradeConfig(t.TempDir(), true); err != nil {
		t.Fatal("fresh install", err)
	}
}

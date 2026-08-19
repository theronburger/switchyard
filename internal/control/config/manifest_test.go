package config

import (
	"reflect"
	"testing"
)

func TestParseLocalManifestReadsStrictSchemaAndDisplaySettings(t *testing.T) {
	contents := []byte(`
# Personal repository configuration
schemaVersion: 1
adapter: marketplace
display:
  name: "Marketplace # local"
`)
	manifest, err := ParseLocalManifest(contents)
	if err != nil {
		t.Fatal(err)
	}
	want := LocalManifest{
		SchemaVersion: 1,
		Adapter:       "marketplace",
		Display:       DisplaySettings{Name: "Marketplace # local"},
	}
	if !reflect.DeepEqual(manifest, want) {
		t.Fatalf("manifest: got %#v, want %#v", manifest, want)
	}
}

func TestParseLocalManifestRejectsUnsupportedOrAmbiguousYAML(t *testing.T) {
	tests := map[string]string{
		"empty":              "",
		"unsupported schema": "schemaVersion: 2\nadapter: marketplace\ndisplay:\n  name: Marketplace\n",
		"duplicate":          "schemaVersion: 1\nadapter: marketplace\nadapter: other\ndisplay:\n  name: Marketplace\n",
		"unknown":            "schemaVersion: 1\nadapter: marketplace\nsecret: value\ndisplay:\n  name: Marketplace\n",
		"anchor":             "schemaVersion: 1\nadapter: &adapter marketplace\ndisplay:\n  name: Marketplace\n",
		"tab":                "schemaVersion: 1\nadapter: marketplace\ndisplay:\n\tname: Marketplace\n",
		"invalid adapter":    "schemaVersion: 1\nadapter: Marketplace!\ndisplay:\n  name: Marketplace\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseLocalManifest([]byte(contents)); err == nil {
				t.Fatal("expected invalid manifest to fail")
			}
		})
	}
}

func TestParseLocalManifestAllowsDefaultDisplaySettings(t *testing.T) {
	manifest, err := ParseLocalManifest([]byte("schemaVersion: 1\nadapter: marketplace\n"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Display.Name != "" {
		t.Fatalf("display name: got %q, want empty default", manifest.Display.Name)
	}
}

func TestParseLocalManifestReadsRuntimeCatalog(t *testing.T) {
	manifest, err := ParseLocalManifest([]byte(`
schemaVersion: 1
adapter: marketplace
runtime:
  defaultTarget: testing
  targets:
    - development
    - testing
    - demo
    - production
  warnOnStart:
    - demo
    - production
  services:
    - app
    - organizer
    - nonprofit-service
`))
	if err != nil {
		t.Fatal(err)
	}
	want := RuntimeSettings{
		DefaultTarget:      "testing",
		Targets:            []string{"development", "testing", "demo", "production"},
		WarnOnStartTargets: []string{"demo", "production"},
		Services:           []string{"app", "organizer", "nonprofit-service"},
	}
	if !reflect.DeepEqual(manifest.Runtime, want) {
		t.Fatalf("runtime settings: got %#v, want %#v", manifest.Runtime, want)
	}
}

func TestParseLocalManifestRejectsInvalidRuntimeCatalog(t *testing.T) {
	tests := map[string]string{
		"missing default": `schemaVersion: 1
adapter: marketplace
runtime:
  targets:
    - testing
  services:
    - organizer
`,
		"default not listed": `schemaVersion: 1
adapter: marketplace
runtime:
  defaultTarget: production
  targets:
    - testing
  services:
    - organizer
`,
		"duplicate service": `schemaVersion: 1
adapter: marketplace
runtime:
  defaultTarget: testing
  targets:
    - testing
  services:
    - organizer
    - organizer
`,
		"invalid list syntax": `schemaVersion: 1
adapter: marketplace
runtime:
  defaultTarget: testing
  targets: testing
  services:
    - organizer
`,
		"warn target not listed": `schemaVersion: 1
adapter: marketplace
runtime:
  defaultTarget: testing
  targets:
    - testing
  warnOnStart:
    - production
  services:
    - organizer
`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseLocalManifest([]byte(contents)); err == nil {
				t.Fatal("expected invalid runtime settings to fail")
			}
		})
	}
}

func TestParseLocalManifestReadsRepositoryNeutralWorkspaceDefaults(t *testing.T) {
	manifest, err := ParseLocalManifest([]byte(`
schemaVersion: 1
adapter: go-service
workspace:
  managedRoot: /Users/example/Developer/go-service-worktrees
  defaultBase: origin/trunk
`))
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceSettings{
		ManagedRoot: "/Users/example/Developer/go-service-worktrees",
		DefaultBase: "origin/trunk",
	}
	if !reflect.DeepEqual(manifest.Workspace, want) {
		t.Fatalf("workspace settings: got %#v want %#v", manifest.Workspace, want)
	}
}

func TestParseLocalManifestRejectsUnsafeWorkspaceDefaults(t *testing.T) {
	for name, contents := range map[string]string{
		"relative root":   "schemaVersion: 1\nadapter: marketplace\nworkspace:\n  managedRoot: ../foreign\n",
		"root filesystem": "schemaVersion: 1\nadapter: marketplace\nworkspace:\n  managedRoot: /\n",
		"unknown field":   "schemaVersion: 1\nadapter: marketplace\nworkspace:\n  packageManager: yarn\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseLocalManifest([]byte(contents)); err == nil {
				t.Fatal("unsafe workspace configuration was accepted")
			}
		})
	}
}

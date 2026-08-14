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

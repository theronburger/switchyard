package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRealDirectoryWithinRefusesSymlinkedComponents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for relative, want := range map[string]bool{
		".": true, "apps": true, "apps/web": true,
		"escape": false, "escape/sub": false, "..": false, "../x": false, "apps/../..": false,
		"/abs": false, "apps/": false, "./apps": false, "file": false, "missing": false, "": false,
	} {
		path, ok := RealDirectoryWithin(root, relative)
		if ok != want {
			t.Errorf("%q: ok=%v want %v (path %q)", relative, ok, want, path)
		}
		if ok && path != filepath.Join(root, relative) && (relative != "." || path != root) {
			t.Errorf("%q resolved to %q", relative, path)
		}
	}
	if _, ok := RealDirectoryWithin(filepath.Join(root, "missing-root"), "."); ok {
		t.Error("missing root accepted")
	}
	if _, ok := RealDirectoryWithin("relative", "."); ok {
		t.Error("relative root accepted")
	}
}

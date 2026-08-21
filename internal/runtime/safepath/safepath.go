// Package safepath proves that a relative path resolves to a real directory
// physically inside a root, not merely lexically: a symlink committed inside a
// checkout (for example apps -> /Users/me) must never redirect an accepted
// command's working directory outside the worktree it was accepted for.
package safepath

import (
	"os"
	"path/filepath"
	"strings"
)

// RealDirectoryWithin joins relative onto root and returns the result only
// when relative is clean and non-absolute, does not climb out of root, and
// every component beneath root exists as a real directory that is not a
// symlink. "." names root itself.
func RealDirectoryWithin(root, relative string) (string, bool) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative ||
		strings.ContainsRune(relative, 0) {
		return "", false
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return "", false
	}
	if relative == "." {
		return root, true
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
	}
	return current, true
}

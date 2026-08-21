package helperenv

import (
	"slices"
	"testing"
)

func TestFilterDropsEverythingOutsideTheAllowlist(t *testing.T) {
	source := []string{
		"HOME=/Users/example",
		"PATH=/usr/bin:/bin",
		"SWITCHYARD_TOKEN=must-not-cross",
		"AWS_SECRET_ACCESS_KEY=must-not-cross",
		"GITHUB_TOKEN=must-not-cross",
		"HTTPS_PROXY=https://user:secret@example.invalid",
		"MALFORMED",
		"=empty-name",
		"TMPDIR=/tmp/a\x00b",
		"LANG=en_US.UTF-8",
		"HOME=/Users/override",
	}
	filtered := Filter(source)
	want := []string{"HOME=/Users/override", "LANG=en_US.UTF-8", "PATH=/usr/bin:/bin"}
	if !slices.Equal(filtered, want) {
		t.Fatalf("filtered environment: %v", filtered)
	}
	for _, entry := range filtered {
		if slices.Contains([]string{"SWITCHYARD_TOKEN", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN"}, entry) ||
			entry == "MALFORMED" || entry == "=empty-name" {
			t.Fatalf("forbidden entry crossed: %s", entry)
		}
	}
}

func TestSanitizedNeverReturnsUnlistedNames(t *testing.T) {
	t.Setenv("SWITCHYARD_SENTINEL_SECRET", "must-not-cross")
	t.Setenv("HOME", t.TempDir())
	for _, entry := range Sanitized() {
		name, _, _ := cutName(entry)
		if !Allowed(name) {
			t.Fatalf("unlisted name crossed: %s", name)
		}
	}
}

func cutName(entry string) (string, string, bool) {
	for index := 0; index < len(entry); index++ {
		if entry[index] == '=' {
			return entry[:index], entry[index+1:], true
		}
	}
	return entry, "", false
}

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type manifestResponse struct {
	contents []byte
	exists   bool
	err      error
}

type fakeManifestSource struct {
	responses map[string]manifestResponse
	paths     []string
}

func (source *fakeManifestSource) ReadManifest(
	_ context.Context,
	path string,
) ([]byte, bool, error) {
	source.paths = append(source.paths, path)
	response := source.responses[path]
	return response.contents, response.exists, response.err
}

func TestResolverUsesLocalManifestAndFallsBackOnlyWhenAbsent(t *testing.T) {
	source := &fakeManifestSource{responses: map[string]manifestResponse{
		"/Repositories/manifest/.switchyard.yaml": {
			contents: []byte("schemaVersion: 1\nadapter: marketplace\ndisplay:\n  name: Local Marketplace\n"),
			exists:   true,
		},
	}}
	resolver, err := NewResolver(source)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolver.Resolve(context.Background(), []ExplicitRepositoryRoot{
		{RootPath: "/Repositories/manifest", Adapter: "ignored", DisplayName: "Ignored"},
		{RootPath: "/Repositories/fallback", Adapter: "marketplace", DisplayName: "Explicit Marketplace"},
	})
	if len(resolution.Errors) != 0 || len(resolution.Repositories) != 2 {
		t.Fatalf("resolution: %#v", resolution)
	}
	if resolution.Repositories[0].RootPath != "/Repositories/fallback" ||
		resolution.Repositories[0].Source != "explicit" ||
		resolution.Repositories[0].DisplayName != "Explicit Marketplace" {
		t.Fatalf("explicit fallback: %#v", resolution.Repositories[0])
	}
	if resolution.Repositories[1].RootPath != "/Repositories/manifest" ||
		resolution.Repositories[1].Source != "local-manifest" ||
		resolution.Repositories[1].Adapter != "marketplace" ||
		resolution.Repositories[1].DisplayName != "Local Marketplace" {
		t.Fatalf("manifest resolution: %#v", resolution.Repositories[1])
	}
	for _, repository := range resolution.Repositories {
		if repository.RequiredLocalExcludePattern != "/.switchyard.yaml" ||
			!strings.HasSuffix(repository.ManifestPath, "/.switchyard.yaml") {
			t.Fatalf("local-only manifest plan is incomplete: %#v", repository)
		}
	}
}

func TestResolverDoesNotFallBackWhenManifestExistsButIsInvalid(t *testing.T) {
	source := &fakeManifestSource{responses: map[string]manifestResponse{
		"/Repositories/marketplace/.switchyard.yaml": {
			contents: []byte("AWS_SECRET_ACCESS_KEY: should-not-escape\n"),
			exists:   true,
		},
	}}
	resolver, err := NewResolver(source)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolver.Resolve(context.Background(), []ExplicitRepositoryRoot{{
		RootPath: "/Repositories/marketplace", Adapter: "marketplace",
	}})
	if len(resolution.Repositories) != 0 || len(resolution.Errors) != 1 ||
		resolution.Errors[0].Code != "LOCAL_MANIFEST_INVALID" {
		t.Fatalf("resolution: %#v", resolution)
	}
	if strings.Contains(resolution.Errors[0].Error(), "AWS_SECRET") ||
		strings.Contains(resolution.Errors[0].Error(), "should-not-escape") {
		t.Fatalf("manifest data escaped structured error: %q", resolution.Errors[0].Error())
	}
}

func TestResolverDoesNotSurfaceManifestReadErrors(t *testing.T) {
	source := &fakeManifestSource{responses: map[string]manifestResponse{
		"/Repositories/marketplace/.switchyard.yaml": {
			err: errors.New("token=secret https://example.invalid"),
		},
	}}
	resolver, err := NewResolver(source)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolver.Resolve(context.Background(), []ExplicitRepositoryRoot{{
		RootPath: "/Repositories/marketplace", Adapter: "marketplace",
	}})
	if len(resolution.Errors) != 1 || resolution.Errors[0].Code != "LOCAL_MANIFEST_UNREADABLE" {
		t.Fatalf("resolution errors: %#v", resolution.Errors)
	}
	if strings.Contains(resolution.Errors[0].Error(), "token") ||
		strings.Contains(resolution.Errors[0].Error(), "example.invalid") {
		t.Fatalf("read error escaped sanitization: %q", resolution.Errors[0].Error())
	}
}

func TestOSManifestSourceReadsOnlyBoundedRegularFiles(t *testing.T) {
	temporaryDirectory := t.TempDir()
	manifestPath := filepath.Join(temporaryDirectory, LocalManifestFilename)
	source := OSManifestSource{}

	if contents, exists, err := source.ReadManifest(context.Background(), manifestPath); err != nil ||
		exists || contents != nil {
		t.Fatalf("absent manifest: contents %q, exists %t, err %v", contents, exists, err)
	}
	manifestContents := []byte("schemaVersion: 1\nadapter: marketplace\n")
	if err := os.WriteFile(manifestPath, manifestContents, 0o600); err != nil {
		t.Fatal(err)
	}
	contents, exists, err := source.ReadManifest(context.Background(), manifestPath)
	if err != nil || !exists || string(contents) != string(manifestContents) {
		t.Fatalf("regular manifest: contents %q, exists %t, err %v", contents, exists, err)
	}

	linkedPath := filepath.Join(temporaryDirectory, "linked-switchyard.yaml")
	if err := os.Symlink(manifestPath, linkedPath); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := source.ReadManifest(context.Background(), linkedPath); err == nil || !exists {
		t.Fatalf("symlink manifest: exists %t, err %v", exists, err)
	}
}

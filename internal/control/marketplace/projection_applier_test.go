package marketplacecontrol

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theronburger/switchyard/internal/control/environment"
)

func TestProjectionCreateApplyAndRestartSafeRollback(t *testing.T) {
	t.Parallel()
	applier, registry, registration := testProjectionApplier(t, "env_create")
	leases := testLeases("env_create", 20101, 21101, 22101, 22102, 23101)
	change, err := applier.Plan(
		context.Background(),
		"env_create",
		"run_create",
		environment.ProjectionRequest{ID: marketplaceServerlessProjection},
		leases,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(change.RollbackToken) == 0 || len(change.RollbackToken) >
		base64ProjectionTokenLimit() {
		t.Fatalf("rollback token is absent or unbounded: %d", len(change.RollbackToken))
	}
	if err := applier.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(
		registration.WorktreeRoot,
		"services/nonprofit-service/.switchyard.serverless.ts",
	)
	contents, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`// switchyard-environment-id: "env_create"`,
		`const importedConfiguration = require("./serverless.ts")`,
		`const configuration = importedConfiguration.default ?? importedConfiguration`,
		`["httpPort"] = 20101`,
		`["lambdaPort"] = 21101`,
		`["endpoint"] = "http://127.0.0.1:22101"`,
	} {
		if !bytes.Contains(contents, []byte(expected)) {
			t.Fatalf("assigned projection is missing %q:\n%s", expected, contents)
		}
	}
	info, err := os.Lstat(projectionPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("projection mode: info=%v err=%v", info, err)
	}

	restarted, err := NewProjectionApplier(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Rollback(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(projectionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created projection survived rollback: %v", err)
	}
}

func TestProjectionRefusesOwnedContentFromAnotherEnvironment(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "marketplace")
	serviceDirectory := filepath.Join(root, "services", "nonprofit-service")
	if err := os.MkdirAll(serviceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	registration := testRegistration("env_target", root)
	applier, err := NewProjectionApplier(mustRegistry(t, registration))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := renderProjection(
		"env_foreign",
		testLeases("env_foreign", 20201, 21201, 22201, 22202, 23201),
	)
	if err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(root, foreign.RelativePath)
	if err := os.WriteFile(projectionPath, foreign.Contents, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = applier.Plan(
		context.Background(), "env_target", "run_target",
		environment.ProjectionRequest{ID: marketplaceServerlessProjection},
		testLeases("env_target", 20301, 21301, 22301, 22302, 23301),
	)
	if !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("cross-environment replacement: %v", err)
	}
	preserved, readErr := os.ReadFile(projectionPath)
	if readErr != nil || !bytes.Equal(preserved, foreign.Contents) {
		t.Fatalf("cross-environment refusal modified the projection: %v", readErr)
	}
}

func TestProjectionReplaceAndRollbackRestoreExactOwnedContent(t *testing.T) {
	t.Parallel()
	applier, _, registration := testProjectionApplier(t, "env_replace")
	oldLeases := testLeases("env_replace", 20201, 21201, 22201, 22202, 23201)
	oldProjection, err := renderProjection("env_replace", oldLeases)
	if err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(registration.WorktreeRoot, oldProjection.RelativePath)
	if err := os.WriteFile(projectionPath, oldProjection.Contents, 0o600); err != nil {
		t.Fatal(err)
	}
	newLeases := testLeases("env_replace", 20301, 21301, 22301, 22302, 23301)
	change, err := applier.Plan(
		context.Background(), "env_replace", "run_replace",
		environment.ProjectionRequest{ID: marketplaceServerlessProjection}, newLeases,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(updated, oldProjection.Contents) || !bytes.Contains(updated, []byte(`22301`)) {
		t.Fatalf("projection was not replaced with assigned ports:\n%s", updated)
	}
	if err := applier.Rollback(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, oldProjection.Contents) {
		t.Fatal("rollback did not restore exact prior owned content")
	}
}

func TestProjectionRefusesForeignModifiedAndOversizedFiles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		contents func(t *testing.T, environmentID string) []byte
		want     error
	}{
		{
			name: "foreign",
			contents: func(_ *testing.T, _ string) []byte {
				return []byte("module.exports = { credential: 'foreign' }\n")
			},
			want: ErrProjectionConflict,
		},
		{
			name: "modified owned",
			contents: func(t *testing.T, environmentID string) []byte {
				projection, err := renderProjection(
					environmentID,
					testLeases(environmentID, 20401, 21401, 22401, 22402, 23401),
				)
				if err != nil {
					t.Fatal(err)
				}
				return append(projection.Contents, []byte("// modified\n")...)
			},
			want: ErrProjectionConflict,
		},
		{
			name: "oversized",
			contents: func(_ *testing.T, _ string) []byte {
				return bytes.Repeat([]byte{'x'}, maximumProjectionBytes+1)
			},
			want: ErrProjectionUnsafe,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			environmentID := "env_" + strings.ReplaceAll(test.name, " ", "_")
			applier, _, registration := testProjectionApplier(t, environmentID)
			projectionPath := filepath.Join(
				registration.WorktreeRoot,
				"services/nonprofit-service/.switchyard.serverless.ts",
			)
			contents := test.contents(t, environmentID)
			if err := os.WriteFile(projectionPath, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := applier.Plan(
				context.Background(), environmentID, "run_refuse",
				environment.ProjectionRequest{ID: marketplaceServerlessProjection},
				testLeases(environmentID, 20501, 21501, 22501, 22502, 23501),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("refusal error: got %v want %v", err, test.want)
			}
			preserved, readErr := os.ReadFile(projectionPath)
			if readErr != nil || !bytes.Equal(preserved, contents) {
				t.Fatalf("refusal modified the file: err=%v", readErr)
			}
		})
	}
}

func TestProjectionRollbackRefusesPostApplyModification(t *testing.T) {
	t.Parallel()
	applier, _, registration := testProjectionApplier(t, "env_modified")
	change, err := applier.Plan(
		context.Background(), "env_modified", "run_modified",
		environment.ProjectionRequest{ID: marketplaceServerlessProjection},
		testLeases("env_modified", 20601, 21601, 22601, 22602, 23601),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(
		registration.WorktreeRoot,
		"services/nonprofit-service/.switchyard.serverless.ts",
	)
	modified, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	modified = append(modified, []byte("// user content\n")...)
	if err := os.WriteFile(projectionPath, modified, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applier.Rollback(context.Background(), change); !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("modified rollback: %v", err)
	}
	preserved, err := os.ReadFile(projectionPath)
	if err != nil || !bytes.Equal(preserved, modified) {
		t.Fatalf("rollback refusal changed user content: %v", err)
	}
}

func TestProjectionRejectsFinalAndParentSymlinks(t *testing.T) {
	t.Parallel()
	t.Run("final", func(t *testing.T) {
		applier, _, registration := testProjectionApplier(t, "env_final_link")
		foreignPath := filepath.Join(t.TempDir(), "foreign.ts")
		foreignContents := []byte("credential=foreign\n")
		if err := os.WriteFile(foreignPath, foreignContents, 0o600); err != nil {
			t.Fatal(err)
		}
		projectionPath := filepath.Join(
			registration.WorktreeRoot,
			"services/nonprofit-service/.switchyard.serverless.ts",
		)
		if err := os.Symlink(foreignPath, projectionPath); err != nil {
			t.Fatal(err)
		}
		_, err := applier.Plan(
			context.Background(), "env_final_link", "run_link",
			environment.ProjectionRequest{ID: marketplaceServerlessProjection},
			testLeases("env_final_link", 20701, 21701, 22701, 22702, 23701),
		)
		if !errors.Is(err, ErrProjectionUnsafe) {
			t.Fatalf("final symlink: %v", err)
		}
		preserved, _ := os.ReadFile(foreignPath)
		if !bytes.Equal(preserved, foreignContents) {
			t.Fatal("foreign symlink target was modified")
		}
	})

	t.Run("parent", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "tree")
		if err := os.MkdirAll(filepath.Join(root, "services"), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.MkdirAll(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "services", "nonprofit-service")); err != nil {
			t.Fatal(err)
		}
		registration := testRegistration("env_parent_link", root)
		applier, err := NewProjectionApplier(mustRegistry(t, registration))
		if err != nil {
			t.Fatal(err)
		}
		_, err = applier.Plan(
			context.Background(), "env_parent_link", "run_link",
			environment.ProjectionRequest{ID: marketplaceServerlessProjection},
			testLeases("env_parent_link", 20801, 21801, 22801, 22802, 23801),
		)
		if !errors.Is(err, ErrProjectionUnsafe) {
			t.Fatalf("parent symlink: %v", err)
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Fatal("projection traversed the parent symlink")
		}
	})
}

func TestProjectionHonorsCancellationAndRedactsHostileTokens(t *testing.T) {
	t.Parallel()
	applier, _, registration := testProjectionApplier(t, "env_cancel")
	change, err := applier.Plan(
		context.Background(), "env_cancel", "run_cancel",
		environment.ProjectionRequest{ID: marketplaceServerlessProjection},
		testLeases("env_cancel", 20901, 21901, 22901, 22902, 23901),
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applier.Apply(cancelled, change); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled apply: %v", err)
	}
	projectionPath := filepath.Join(
		registration.WorktreeRoot,
		"services/nonprofit-service/.switchyard.serverless.ts",
	)
	if _, err := os.Lstat(projectionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled apply created a file: %v", err)
	}

	hostile := change
	hostile.RollbackToken = "AWS_SECRET_ACCESS_KEY=secret-token@example.invalid"
	err = applier.Apply(context.Background(), hostile)
	if !errors.Is(err, ErrProjectionInvalid) || strings.Contains(strings.ToLower(err.Error()), "secret") ||
		strings.Contains(strings.ToLower(err.Error()), "token@") {
		t.Fatalf("hostile token error leaked input: %v", err)
	}
}

func testProjectionApplier(
	t *testing.T,
	environmentID string,
) (ProjectionApplier, EnvironmentRegistry, EnvironmentRegistration) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "marketplace tree")
	if err := os.MkdirAll(filepath.Join(root, "services", "nonprofit-service"), 0o700); err != nil {
		t.Fatal(err)
	}
	registration := testRegistration(environmentID, root)
	registry := mustRegistry(t, registration)
	applier, err := NewProjectionApplier(registry)
	if err != nil {
		t.Fatal(err)
	}
	return applier, registry, registration
}

func base64ProjectionTokenLimit() int {
	return (maximumRollbackTokenBytes*8 + 5) / 6
}

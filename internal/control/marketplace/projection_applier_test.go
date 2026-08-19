package marketplacecontrol

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

func TestEndpointShimRewritesOnlyTheDeclaredFixedEndpoint(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is not part of Switchyard's baseline toolchain")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("switchyard-endpoint-ok"))
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	projection, err := renderEndpointShim("env_node", "service/.switchyard.endpoints.cjs", []endpointRewrite{
		{FromHost: "0.0.0.0", FromPort: 9324, ToHost: "127.0.0.1", ToPort: port},
	})
	if err != nil {
		t.Fatal(err)
	}
	shimPath := filepath.Join(t.TempDir(), ".switchyard.endpoints.cjs")
	if err := os.WriteFile(shimPath, projection.Contents, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", `
const http = require("node:http")
http.get({host: "0.0.0.0", port: 9324, path: "/"}, response => {
  let body = ""
  response.on("data", chunk => { body += chunk })
  response.on("end", () => { if (body !== "switchyard-endpoint-ok") process.exit(2) })
}).on("error", () => process.exit(3))
`)
	command.Env = append(os.Environ(), "NODE_OPTIONS=--require="+shimPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("endpoint shim did not rewrite the fixed address: %v %s", err, output)
	}
}

func TestEnvironmentShimLayersAssignedTargetAndLocalFallback(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is not part of Switchyard's baseline toolchain")
	}
	root := t.TempDir()
	repositoryRoot := filepath.Join(t.TempDir(), "primary checkout")
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{
		filepath.Join(root, "node_modules", "dotenv-flow"),
		filepath.Join(root, "node_modules", "dotenv-expand"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".env.js": `process.env.TARGET_ONLY ??= "from-target"
process.env.PRECEDENCE ??= "from-target"
process.env.EXPLICIT_ROUTE ??= "from-target"
`,
		"node_modules/dotenv-flow/index.js": `module.exports.config = options => {
	  const parsed = options.node_env === "development" ? {
	    PRECEDENCE: "from-development",
	    FALLBACK_ONLY: "from-development",
	    EXPLICIT_ROUTE: "from-development",
	  } : {
	    SHARED_TARGET_ONLY: "from-shared-target",
	    PRECEDENCE: "from-shared-target",
	    EXPLICIT_ROUTE: "from-shared-target",
	  }
  for (const [name, value] of Object.entries(parsed)) {
    if (!(name in process.env)) process.env[name] = value
  }
  return { parsed }
}
`,
		"node_modules/dotenv-expand/index.js": `module.exports.expand = result => result
`,
	}
	for relativePath, contents := range files {
		if err := os.WriteFile(filepath.Join(root, relativePath), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := renderEnvironmentShim("env_layers", repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"DEED_API_SECRET", "GOOGLE_ADDRESS_VALIDATION_API_KEY", "from-development", "from-target", "from-shared-target",
	} {
		if bytes.Contains(projection.Contents, []byte(forbidden)) {
			t.Fatalf("environment shim embedded runtime data %q", forbidden)
		}
	}
	shimPath := filepath.Join(root, marketplaceEnvironmentShim)
	if err := os.WriteFile(shimPath, projection.Contents, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(node, "-e", `
if (process.env.EXPLICIT_ROUTE !== "from-switchyard") process.exit(2)
if (process.env.PRECEDENCE !== "from-shared-target") process.exit(3)
if (process.env.TARGET_ONLY !== "from-target") process.exit(4)
if (process.env.FALLBACK_ONLY !== "from-development") process.exit(5)
if (process.env.SHARED_TARGET_ONLY !== "from-shared-target") process.exit(6)
`)
	command.Dir = root
	command.Env = []string{
		"DEPLOYMENT_ENVIRONMENT=testing",
		"EXPLICIT_ROUTE=from-switchyard",
		"NODE_OPTIONS=--require=" + shimPath,
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("environment layering failed: %v %s", err, output)
	}
}

func TestProjectionAppliesAndRollsBackEverySelectedServiceAndSlackShim(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "marketplace-all-projections")
	for _, directory := range []string{"services/donation-batch-service", "services/nonprofit-service", "services/report-service", "services/slack-service"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	registration := testRegistration("env_bundle", root)
	applier, err := NewProjectionApplier(mustRegistry(t, registration))
	if err != nil {
		t.Fatal(err)
	}
	catalog := marketplaceadapter.DefaultCatalog()
	assigned := make(map[string]map[string]int)
	leases := make([]portlease.Lease, 0)
	nextPort := 31000
	for _, serviceID := range []string{"donation-batch-service", "nonprofit-service", "report-service", "slack-service"} {
		definition, found := catalog.Definition(serviceID)
		if !found {
			t.Fatalf("definition %q is missing", serviceID)
		}
		assigned[serviceID] = make(map[string]int)
		for _, requirement := range definition.PortRequirements {
			assigned[serviceID][requirement.Purpose] = nextPort
			leases = append(leases, portlease.Lease{
				Key:  portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: serviceID, Purpose: requirement.Purpose},
				Host: "127.0.0.1", Port: nextPort,
			})
			nextPort++
		}
	}
	change, err := applier.Plan(context.Background(), registration.EnvironmentID, "run_bundle",
		environment.ProjectionRequest{ID: marketplaceServerlessProjection}, leases)
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	expectations := map[string][]string{
		".switchyard.env.cjs": {
			`require(path.join(root, ".env.js"))`,
			`node_env: "development"`,
		},
		"services/donation-batch-service/.switchyard.serverless.ts": {
			`["endpoint"] = "http://127.0.0.1:` + strconv.Itoa(assigned["donation-batch-service"]["elasticmq-rest"]) + `"`,
		},
		"services/donation-batch-service/.switchyard.endpoints.cjs": {
			`"fromHost":"0.0.0.0","fromPort":9324`,
			`"toPort":` + strconv.Itoa(assigned["donation-batch-service"]["elasticmq-rest"]),
		},
		"services/nonprofit-service/.switchyard.serverless.ts": {
			`["host"] = "127.0.0.1"`,
			`["endpoint"] = "http://127.0.0.1:` + strconv.Itoa(assigned["nonprofit-service"]["elasticmq-rest"]) + `"`,
		},
		"services/report-service/.switchyard.serverless.ts": {
			`configuration["plugins"].push("serverless-offline-sqs")`,
			`["AI_INSIGHT_JOB_QUEUE_URL"] = "http://127.0.0.1:` + strconv.Itoa(assigned["report-service"]["elasticmq-rest"]) + `/queue/local-report-service-ai-insight-job"`,
		},
		"services/slack-service/.switchyard.serverless.ts": {
			`["serverless-offline-aws-eventbridge"]`,
			`["endpoint"] = "http://127.0.0.1:` + strconv.Itoa(assigned["slack-service"]["elasticmq-rest"]) + `"`,
		},
		"services/slack-service/.switchyard.endpoints.cjs": {
			`const originalRequest = http.request`,
			`"toPort":` + strconv.Itoa(assigned["slack-service"]["dynamodb"]),
		},
	}
	for relativePath, fragments := range expectations {
		contents, readError := os.ReadFile(filepath.Join(root, relativePath))
		if readError != nil {
			t.Fatal(readError)
		}
		for _, fragment := range fragments {
			if !bytes.Contains(contents, []byte(fragment)) {
				t.Fatalf("projection %q is missing %q:\n%s", relativePath, fragment, contents)
			}
		}
	}
	if err := applier.Rollback(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	for relativePath := range expectations {
		if _, statError := os.Lstat(filepath.Join(root, relativePath)); !errors.Is(statError, os.ErrNotExist) {
			t.Fatalf("projection %q survived rollback: %v", relativePath, statError)
		}
	}
}

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
		registration.RepositoryRoot,
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
	oldProjection, err := renderProjection("env_replace", registration.RepositoryRoot, oldLeases)
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
					t.TempDir(),
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

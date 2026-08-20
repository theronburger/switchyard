package contractv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusFixture(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}

	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot.Environments[0].DisplayName, "Demo environment"; got != want {
		t.Fatalf("display name: got %q, want %q", got, want)
	}
	if observation := snapshot.Repositories[0].Observation; observation == nil || observation.Stale || observation.ObservedAt == nil {
		t.Fatalf("repository observation did not decode: %#v", observation)
	}
	run := snapshot.Environments[0].Services[0].Run
	if run == nil || run.SourceRevision != snapshot.Repositories[0].Worktrees[0].HeadRevision ||
		!run.SourceHasTrackedChanges || run.SourceObservedAt.IsZero() {
		t.Fatalf("run source provenance did not decode: %#v", run)
	}
	if snapshot.Operations[0].RunID != run.ID {
		t.Fatalf("operation run id %q does not match service run %q", snapshot.Operations[0].RunID, run.ID)
	}
}

func TestStatusAcceptsRepositoryNeutralWorkspaceToolchains(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	worktree := &snapshot.Repositories[0].Worktrees[0]
	worktree.Workspace = &WorkspaceStatus{
		Ownership: "adopted", State: "ready",
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PreparedAt:  time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
		Toolchains: []WorkspaceToolchain{
			{ID: "go", RequestedVersion: "1.26", ResolvedVersion: "1.26.5"},
			{ID: "node", RequestedVersion: "24", ResolvedVersion: "24.19.0"},
		},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	worktree.Workspace.Toolchains = nil
	if err := snapshot.Validate(); err == nil {
		t.Fatal("null workspace toolchains were accepted")
	}
}

func TestStatusFixtureAllowsAdditiveFields(t *testing.T) {
	contents := []byte(`{
		"schemaVersion": 2,
		"snapshotRevision": 1,
		"generatedAt": "2026-08-14T10:00:00Z",
		"futureField": {"ignored": true},
		"daemon": {
			"instanceId": "daemon_test",
			"version": "test",
			"state": "ready",
			"startedAt": "2026-08-14T10:00:00Z"
		},
		"repositories": [],
		"environments": [],
		"operations": [],
		"alerts": []
	}`)

	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStatusRejectsNullCollections(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "repositories", mutate: func(value map[string]any) { value["repositories"] = nil }},
		{name: "worktrees", mutate: func(value map[string]any) {
			value["repositories"].([]any)[0].(map[string]any)["worktrees"] = nil
		}},
		{name: "services", mutate: func(value map[string]any) {
			value["environments"].([]any)[0].(map[string]any)["services"] = nil
		}},
		{name: "urls", mutate: func(value map[string]any) {
			value["environments"].([]any)[0].(map[string]any)["urls"] = nil
		}},
		{name: "service port leases", mutate: func(value map[string]any) {
			environment := value["environments"].([]any)[0].(map[string]any)
			environment["services"].([]any)[0].(map[string]any)["portLeaseIds"] = nil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(contents, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value)
			mutated, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot StatusSnapshot
			if err := json.Unmarshal(mutated, &snapshot); err != nil {
				t.Fatal(err)
			}
			if err := snapshot.Validate(); err == nil {
				t.Fatal("null collection was accepted")
			}
		})
	}
}

func TestStatusRejectsEnvironmentTargetOutsideRepositoryCatalog(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Environments[0].TargetID = "unknown"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("environment target outside the repository catalog was accepted")
	}
}

func TestStatusValidatesLifecycleVocabularyAndAcceptsLegacyPersistedStates(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	environment := &snapshot.Environments[0]
	environment.DesiredState = "failed"
	environment.ObservedState = "degraded"
	environment.Services[0].DesiredState = "failed"
	environment.Services[0].ObservedState = "unverifiable"
	environment.Services[0].ObservationCode = "PROCESS_OWNERSHIP_UNVERIFIED"
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("legacy persisted state was rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Environment)
	}{
		{name: "environment desired", mutate: func(environment *Environment) { environment.DesiredState = "surprising" }},
		{name: "environment observed", mutate: func(environment *Environment) { environment.ObservedState = "surprising" }},
		{name: "environment health", mutate: func(environment *Environment) { environment.Health = "surprising" }},
		{name: "service desired", mutate: func(environment *Environment) { environment.Services[0].DesiredState = "surprising" }},
		{name: "service observed", mutate: func(environment *Environment) { environment.Services[0].ObservedState = "surprising" }},
		{name: "service health", mutate: func(environment *Environment) { environment.Services[0].Health = "surprising" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var candidate StatusSnapshot
			if err := json.Unmarshal(contents, &candidate); err != nil {
				t.Fatal(err)
			}
			test.mutate(&candidate.Environments[0])
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid lifecycle value was accepted")
			}
		})
	}
}

func TestStatusRejectsInconsistentWorktreeLineAttribution(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Repositories[0].Worktrees[0].Changes.Services[0].Committed.Additions++
	if err := snapshot.Validate(); err == nil {
		t.Fatal("inconsistent service line attribution was accepted")
	}
}

func TestStatusRejectsInconsistentPullRequestChecks(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", "status.json")
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	checks := &snapshot.Repositories[0].Worktrees[0].PullRequest.PullRequest.Checks
	checks.State = "passing"
	if err := snapshot.Validate(); err == nil {
		t.Fatal("passing state with a pending check was accepted")
	}

	if err := json.Unmarshal(contents, &snapshot); err != nil {
		t.Fatal(err)
	}
	checks = &snapshot.Repositories[0].Worktrees[0].PullRequest.PullRequest.Checks
	checks.Pending = -1
	if err := snapshot.Validate(); err == nil {
		t.Fatal("negative check count was accepted")
	}
}

func TestTransportFixtures(t *testing.T) {
	tests := []struct {
		name string
		file string
		into any
	}{
		{name: "runtime", file: "runtime.json", into: &RuntimeDescriptor{}},
		{name: "handshake", file: "handshake.json", into: &Handshake{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", test.file)
			contents, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(contents, test.into); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMutationFixtures(t *testing.T) {
	tests := []struct {
		file  string
		value any
	}{
		{
			file:  "start-environment-request.json",
			value: &StartEnvironmentRequest{},
		},
		{
			file:  "stop-environment-request.json",
			value: &StopEnvironmentRequest{},
		},
		{
			file:  "mutation-receipt.json",
			value: &MutationReceipt{},
		},
	}

	for _, test := range tests {
		t.Run(test.file, func(t *testing.T) {
			fixturePath := filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", test.file)
			contents, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(contents, test.value); err != nil {
				t.Fatal(err)
			}
			switch value := test.value.(type) {
			case *StartEnvironmentRequest:
				err = value.Validate()
			case *StopEnvironmentRequest:
				err = value.Validate()
			case *MutationReceipt:
				err = value.Validate()
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStartEnvironmentRequestRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	valid := StartEnvironmentRequest{
		MutationRequest: MutationRequest{
			SchemaVersion:  SchemaVersion,
			RequestID:      "request_test",
			IdempotencyKey: "start:test",
		},
		WorktreeID: "worktree_test",
		ServiceIDs: []string{"organizer"},
	}

	tests := []struct {
		name   string
		mutate func(*StartEnvironmentRequest)
	}{
		{name: "future schema", mutate: func(request *StartEnvironmentRequest) {
			request.SchemaVersion++
		}},
		{name: "control in request id", mutate: func(request *StartEnvironmentRequest) {
			request.RequestID = "request\nleak"
		}},
		{name: "negative revision", mutate: func(request *StartEnvironmentRequest) {
			revision := int64(-1)
			request.ExpectedEnvironmentRevision = &revision
		}},
		{name: "null services", mutate: func(request *StartEnvironmentRequest) {
			request.ServiceIDs = nil
		}},
		{name: "duplicate services", mutate: func(request *StartEnvironmentRequest) {
			request.ServiceIDs = []string{"organizer", "organizer"}
		}},
		{name: "mismatched target confirmation", mutate: func(request *StartEnvironmentRequest) {
			request.TargetID = "demo"
			request.ConfirmedTargetID = "production"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.ServiceIDs = append([]string(nil), valid.ServiceIDs...)
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("invalid mutation request was accepted")
			}
		})
	}
}

func TestPrepareWorktreeRequestValidation(t *testing.T) {
	request := PrepareWorktreeRequest{
		MutationRequest: MutationRequest{
			SchemaVersion: SchemaVersion, RequestID: "request_prepare", IdempotencyKey: "prepare:test",
		},
		WorktreeID: "worktree_test",
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	revision := int64(1)
	request.ExpectedEnvironmentRevision = &revision
	if err := request.Validate(); err == nil {
		t.Fatal("environment revision was accepted for workspace preparation")
	}
	request.ExpectedEnvironmentRevision = nil
	request.WorktreeID = " bad"
	if err := request.Validate(); err == nil {
		t.Fatal("unsafe worktree id was accepted")
	}
}

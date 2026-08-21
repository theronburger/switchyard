package contractv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "v2", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func decodeStrictFixture(t *testing.T, name string, destination any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(readFixture(t, name))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// TestConfigurationProfileActionAndDiagnosticsFixtures freezes the v2 shapes
// that 0.1.0 did not have fixtures for. Every fixture must decode strictly
// (no unknown fields, so the Swift models and these Go types agree on every
// key) and pass the same validation the daemon and clients apply.
func TestConfigurationProfileActionAndDiagnosticsFixtures(t *testing.T) {
	var status ConfigurationStatus
	decodeStrictFixture(t, "configuration-status.json", &status)
	if err := status.Validate(); err != nil {
		t.Fatal(err)
	}
	if status.State != "pending" || status.Candidate == nil || status.Desired == nil || len(status.Desired.Repositories) != 1 {
		t.Fatalf("configuration status fixture lost its shape: %+v", status)
	}
	if status.Desired.Repositories[0].Key != "sample" || status.Candidate.RepositoryDigests["sample"] == "" {
		t.Fatalf("configuration status fixture repository entry changed: %+v", status.Desired.Repositories[0])
	}

	var mutation ConfigurationRepositoryMutationRequest
	decodeStrictFixture(t, "configuration-repository-mutation-request.json", &mutation)
	if err := mutation.Validate(); err != nil {
		t.Fatal(err)
	}
	if mutation.Operation != "upsert" || mutation.Entry == nil || mutation.ExpectedSourceDigest != status.Desired.SourceDigest {
		t.Fatalf("configuration mutation fixture drifted from the status fixture: %+v", mutation)
	}

	var actions ProfileActionList
	decodeStrictFixture(t, "profile-action-list.json", &actions)
	if err := actions.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(actions.Actions) != 3 || actions.Actions[0].Kind != "lifecycle" || actions.Actions[0].Lifecycle != "prepare" ||
		actions.Actions[2].Risk != "remote-write" || !actions.Actions[2].RequiresConfirmation {
		t.Fatalf("profile action fixture lost its shape: %+v", actions.Actions)
	}

	var run RunProfileActionRequest
	decodeStrictFixture(t, "run-profile-action-request.json", &run)
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	if run.ActionID != "publish-preview" || run.ConfirmedActionID != run.ActionID || run.WorktreeID == "" || run.EnvironmentID != "" {
		t.Fatalf("run profile action fixture lost its worktree-scoped confirmed shape: %+v", run)
	}

	var diagnostics OperationDiagnostics
	decodeStrictFixture(t, "operation-diagnostics.json", &diagnostics)
	if diagnostics.SchemaVersion != SchemaVersion || diagnostics.OperationID == "" || diagnostics.LogReference == "" ||
		len(diagnostics.Excerpts) != 2 || diagnostics.Excerpts[0].Stream != "stdout" || diagnostics.Excerpts[1].Stream != "stderr" ||
		!diagnostics.Excerpts[1].Truncated || !diagnostics.Excerpts[1].Redacted {
		t.Fatalf("diagnostics fixture lost its shape: %+v", diagnostics)
	}
	if strings.HasPrefix(diagnostics.LogReference, "/") || strings.Contains(diagnostics.LogReference, "..") {
		t.Fatalf("diagnostics fixture log reference must stay opaque and relative: %q", diagnostics.LogReference)
	}
}

func TestOccupancyFixtures(t *testing.T) {
	var lease OccupancyLease
	decodeStrictFixture(t, "occupancy-lease.json", &lease)
	if err := lease.Validate(); err != nil {
		t.Fatal(err)
	}
	if lease.State != "held" || lease.ReleasedAt != nil {
		t.Fatalf("occupancy lease fixture must be a held lease: %+v", lease)
	}

	var acquire AcquireOccupancyRequest
	decodeStrictFixture(t, "acquire-occupancy-request.json", &acquire)
	if err := acquire.Validate(); err != nil {
		t.Fatal(err)
	}
	var release ReleaseOccupancyRequest
	decodeStrictFixture(t, "release-occupancy-request.json", &release)
	if err := release.Validate(); err != nil {
		t.Fatal(err)
	}
	if acquire.WorktreeID != lease.WorktreeID || release.WorktreeID != lease.WorktreeID || release.LeaseID != lease.ID ||
		acquire.HolderKind != lease.HolderKind || acquire.HolderLabel != lease.HolderLabel {
		t.Fatal("occupancy fixtures describe different leases")
	}

	// The status fixture's worktree must accept the lease so the projection
	// validates with occupancy attached.
	var snapshot StatusSnapshot
	if err := json.Unmarshal(readFixture(t, "status.json"), &snapshot); err != nil {
		t.Fatal(err)
	}
	attached := false
	for repositoryIndex := range snapshot.Repositories {
		for worktreeIndex := range snapshot.Repositories[repositoryIndex].Worktrees {
			worktree := &snapshot.Repositories[repositoryIndex].Worktrees[worktreeIndex]
			if worktree.ID == lease.WorktreeID {
				worktree.Occupancy = []OccupancyLease{lease}
				attached = true
			}
		}
	}
	if !attached {
		t.Fatalf("occupancy fixture names a worktree missing from status.json: %q", lease.WorktreeID)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	// Publishing a released lease, a foreign worktree's lease, or a duplicate
	// on a worktree is rejected.
	released := lease
	now := lease.AcquiredAt.Add(1)
	released.State, released.ReleasedAt = "released", &now
	for name, occupancy := range map[string][]OccupancyLease{
		"released":   {released},
		"foreign":    {{ID: lease.ID, WorktreeID: "worktree_other", HolderKind: lease.HolderKind, HolderLabel: lease.HolderLabel, State: "held", AcquiredAt: lease.AcquiredAt}},
		"duplicated": {lease, lease},
	} {
		snapshot.Repositories[0].Worktrees[0].Occupancy = occupancy
		if err := snapshot.Validate(); err == nil {
			t.Fatalf("%s occupancy was accepted", name)
		}
	}
}

func TestUpgradeRequiredErrorFixture(t *testing.T) {
	var envelope struct {
		SchemaVersion int           `json:"schemaVersion"`
		Error         ContractError `json:"error"`
	}
	decodeStrictFixture(t, "upgrade-required-error.json", &envelope)
	if envelope.SchemaVersion != SchemaVersion || envelope.Error.Code != UpgradeRequiredCode || envelope.Error.Retryable ||
		envelope.Error.CurrentState != "2" || envelope.Error.RequestedState != "1" || envelope.Error.NextAction != "upgrade_client" {
		t.Fatalf("upgrade-required fixture drifted: %+v", envelope)
	}
}

func TestOccupancyValidationRejectsUnsafeHolders(t *testing.T) {
	valid := AcquireOccupancyRequest{
		SchemaVersion: SchemaVersion, RequestID: "request_1", WorktreeID: "worktree_1",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*AcquireOccupancyRequest){
		"wrong schema":       func(r *AcquireOccupancyRequest) { r.SchemaVersion = 1 },
		"empty request":      func(r *AcquireOccupancyRequest) { r.RequestID = "" },
		"uppercase kind":     func(r *AcquireOccupancyRequest) { r.HolderKind = "Agent" },
		"kind with spaces":   func(r *AcquireOccupancyRequest) { r.HolderKind = "agent task" },
		"kind leading dash":  func(r *AcquireOccupancyRequest) { r.HolderKind = "-agent" },
		"kind trailing dash": func(r *AcquireOccupancyRequest) { r.HolderKind = "agent-" },
		"kind too long":      func(r *AcquireOccupancyRequest) { r.HolderKind = strings.Repeat("a", 65) },
		"label with path":    func(r *AcquireOccupancyRequest) { r.HolderLabel = "/Users/example" },
		"label backslash":    func(r *AcquireOccupancyRequest) { r.HolderLabel = "C:\\work" },
		"label control":      func(r *AcquireOccupancyRequest) { r.HolderLabel = "Agent\ntask" },
		"label too long":     func(r *AcquireOccupancyRequest) { r.HolderLabel = strings.Repeat("a", 257) },
		"label padded":       func(r *AcquireOccupancyRequest) { r.HolderLabel = " Agent task" },
	}
	for name, mutate := range cases {
		request := valid
		mutate(&request)
		if err := request.Validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

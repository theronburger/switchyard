package processhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestUnverifiedIntentAndLogsAreReportOnly(t *testing.T) {
	ownershipPath, _ := writeSafetyIntent(t, nil)
	runDirectory := filepath.Dir(ownershipPath)
	if err := os.WriteFile(filepath.Join(runDirectory, StdoutLogFileName), []byte("possibly launched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{}, Signaler: signaler})

	observation, err := host.Observe(context.Background(), ownershipPath)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != StateOrphanUnverified || observation.OwnershipVerified ||
		!observation.HasLaunchIntent || !observation.HasLogEvidence || observation.HasProcessEvidence {
		t.Fatalf("orphan observation: %+v", observation)
	}
	stopped, err := host.Stop(context.Background(), ownershipPath)
	if !errors.Is(err, ErrOrphanUnverified) || stopped.State != StateOrphanUnverified {
		t.Fatalf("stop result: observation=%+v error=%v", stopped, err)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("unverified intent sent signals: %v", signaler.signals)
	}
}

func TestUnverifiedCandidateProcessRemainsReportOnly(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	candidate := ProcessIdentity{
		PID: 5000, ParentPID: 1, ProcessGroupID: 5000, StartedAt: startedAt,
		CommandFingerprint: fingerprintCommand("/tmp/helper", []string{"/tmp/helper"}),
	}
	ownershipPath, _ := writeSafetyIntent(t, &candidate)
	snapshot := ProcessSnapshot{Identity: candidate, Status: "running", MemoryBytes: 4096}
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{group: []ProcessSnapshot{snapshot}}, Signaler: signaler})

	observation, err := host.Observe(context.Background(), ownershipPath)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.HasProcessEvidence || observation.MemberCount != 1 {
		t.Fatalf("candidate observation: %+v", observation)
	}
	_, err = host.Stop(context.Background(), ownershipPath)
	if !errors.Is(err, ErrOrphanUnverified) {
		t.Fatalf("stop error: got %v, want %v", err, ErrOrphanUnverified)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("candidate-only evidence sent signals: %v", signaler.signals)
	}
}

func TestUnverifiedIntentRejectsPIDReuseWithoutSignalling(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	candidate := ProcessIdentity{
		PID: 5000, ParentPID: 1, ProcessGroupID: 5000, StartedAt: startedAt,
		CommandFingerprint: fingerprintCommand("/tmp/helper", []string{"/tmp/helper"}),
	}
	ownershipPath, _ := writeSafetyIntent(t, &candidate)
	reused := ProcessSnapshot{Identity: candidate, Status: "running"}
	reused.Identity.StartedAt = startedAt.Add(time.Second)
	reused.Identity.CommandFingerprint = fingerprintCommand("/tmp/foreign", []string{"/tmp/foreign"})
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{group: []ProcessSnapshot{reused}}, Signaler: signaler})

	observation, err := host.Observe(context.Background(), ownershipPath)
	if err != nil {
		t.Fatal(err)
	}
	if observation.HasProcessEvidence {
		t.Fatalf("reused pid was reported as launch evidence: %+v", observation)
	}
	_, err = host.Stop(context.Background(), ownershipPath)
	if !errors.Is(err, ErrOrphanUnverified) {
		t.Fatalf("stop error: got %v, want %v", err, ErrOrphanUnverified)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("reused pid received signals: %v", signaler.signals)
	}
}

type fixedInspector struct {
	group []ProcessSnapshot
}

func (inspector fixedInspector) Inspect(_ context.Context, pid int) (ProcessSnapshot, error) {
	for _, snapshot := range inspector.group {
		if snapshot.Identity.PID == pid {
			return snapshot, nil
		}
	}
	return ProcessSnapshot{}, ErrProcessNotFound
}

func (inspector fixedInspector) ListGroup(context.Context, int) ([]ProcessSnapshot, error) {
	return append([]ProcessSnapshot(nil), inspector.group...), nil
}

type recordingSignaler struct {
	signals []syscall.Signal
}

func (signaler *recordingSignaler) SignalGroup(_ int, signal syscall.Signal) error {
	signaler.signals = append(signaler.signals, signal)
	return nil
}

func TestStopRefusesPIDReuseWithoutSignalling(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	ownershipPath, ownership := writeSafetyOwnership(t, startedAt)
	reusedLeader := snapshotFor(ownership.Leader.PID, 1, ownership.ProcessGroupID, startedAt.Add(time.Second), "foreign")
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{group: []ProcessSnapshot{reusedLeader}}, Signaler: signaler})

	_, err := host.Stop(context.Background(), ownershipPath)
	if !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("stop error: got %v, want %v", err, ErrOwnershipMismatch)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("signalled reused pid: %v", signaler.signals)
	}
}

func TestStopRefusesPGIDReuseWithoutSignalling(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	ownershipPath, ownership := writeSafetyOwnership(t, startedAt)
	reusedGroup := snapshotFor(ownership.Leader.PID, 1, ownership.ProcessGroupID+1, startedAt, ownership.Leader.CommandFingerprint)
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{group: []ProcessSnapshot{reusedGroup}}, Signaler: signaler})

	_, err := host.Stop(context.Background(), ownershipPath)
	if !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("stop error: got %v, want %v", err, ErrOwnershipMismatch)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("signalled reused pgid: %v", signaler.signals)
	}
}

func TestStopRefusesReusedPersistedMemberWithoutSignalling(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	ownershipPath, ownership := writeSafetyOwnership(t, startedAt)
	child := ProcessIdentity{
		PID:                ownership.Leader.PID + 1,
		ParentPID:          ownership.Leader.PID,
		ProcessGroupID:     ownership.ProcessGroupID,
		StartedAt:          startedAt.Add(time.Millisecond),
		CommandFingerprint: "child",
	}
	ownership.Members = append(ownership.Members, child)
	if err := saveOwnership(ownershipPath, ownership); err != nil {
		t.Fatal(err)
	}
	leader := ProcessSnapshot{Identity: ownership.Leader, Status: "running"}
	reusedChild := snapshotFor(child.PID, child.ParentPID, child.ProcessGroupID, child.StartedAt.Add(time.Second), child.CommandFingerprint)
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{group: []ProcessSnapshot{leader, reusedChild}}, Signaler: signaler})

	_, err := host.Stop(context.Background(), ownershipPath)
	if !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("stop error: got %v, want %v", err, ErrOwnershipMismatch)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("signalled reused member: %v", signaler.signals)
	}
}

func TestStopRefusesUnverifiedForeignGroupMemberWithoutSignalling(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	ownershipPath, ownership := writeSafetyOwnership(t, startedAt)
	leader := ProcessSnapshot{Identity: ownership.Leader, Status: "running"}
	foreign := snapshotFor(
		ownership.Leader.PID+1,
		1,
		ownership.ProcessGroupID,
		startedAt.Add(time.Second),
		"foreign",
	)
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{group: []ProcessSnapshot{leader, foreign}}, Signaler: signaler})

	_, err := host.Stop(context.Background(), ownershipPath)
	if !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("stop error: got %v, want %v", err, ErrOwnershipMismatch)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("signalled group containing an unverified foreign process: %v", signaler.signals)
	}
}

func TestCancelledStopDoesNotSignal(t *testing.T) {
	startedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	ownershipPath, ownership := writeSafetyOwnership(t, startedAt)
	signaler := &recordingSignaler{}
	host := New(Config{Inspector: fixedInspector{group: []ProcessSnapshot{{Identity: ownership.Leader, Status: "running"}}}, Signaler: signaler})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := host.Stop(ctx, ownershipPath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stop error: got %v, want %v", err, context.Canceled)
	}
	if len(signaler.signals) != 0 {
		t.Fatalf("cancelled stop sent signals: %v", signaler.signals)
	}
}

func writeSafetyOwnership(t *testing.T, startedAt time.Time) (string, Ownership) {
	t.Helper()
	runDirectory := t.TempDir()
	leader := ProcessIdentity{
		PID:                4000,
		ParentPID:          1,
		ProcessGroupID:     4000,
		StartedAt:          startedAt,
		CommandFingerprint: "leader",
	}
	ownership := Ownership{
		SchemaVersion:     OwnershipSchemaVersion,
		EnvironmentID:     "env_test",
		ServiceID:         "service_test",
		RunID:             "run_test",
		State:             "running",
		ProcessGroupID:    leader.ProcessGroupID,
		Leader:            leader,
		Members:           []ProcessIdentity{leader},
		LaunchFingerprint: "launch",
		StdoutPath:        filepath.Join(runDirectory, StdoutLogFileName),
		StderrPath:        filepath.Join(runDirectory, StderrLogFileName),
		StartedAt:         startedAt,
		UpdatedAt:         startedAt,
	}
	path := filepath.Join(runDirectory, OwnershipFileName)
	if err := saveOwnership(path, ownership); err != nil {
		t.Fatal(err)
	}
	return path, ownership
}

func writeSafetyIntent(t *testing.T, candidate *ProcessIdentity) (string, LaunchIntent) {
	t.Helper()
	runDirectory := t.TempDir()
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	intent := LaunchIntent{
		SchemaVersion: LaunchIntentSchemaVersion, EnvironmentID: "env_test",
		ServiceID: "service_test", RunID: "run_test", Executable: "/tmp/helper",
		LaunchFingerprint: fingerprintCommand("/tmp/helper", []string{"/tmp/helper"}),
		RunDirectory:      runDirectory, CreatedAt: now, UpdatedAt: now,
		CandidateLeader: candidate,
	}
	intentPath := filepath.Join(runDirectory, LaunchIntentFileName)
	if err := saveLaunchIntent(intentPath, intent); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("intent mode: got %04o, want 0600", fileInfo.Mode().Perm())
	}
	return filepath.Join(runDirectory, OwnershipFileName), intent
}

func snapshotFor(pid, parentPID, processGroupID int, startedAt time.Time, fingerprint string) ProcessSnapshot {
	return ProcessSnapshot{
		Identity: ProcessIdentity{
			PID:                pid,
			ParentPID:          parentPID,
			ProcessGroupID:     processGroupID,
			StartedAt:          startedAt,
			CommandFingerprint: fingerprint,
		},
		Status: "running",
	}
}

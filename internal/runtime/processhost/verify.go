package processhost

import (
	"context"
	"fmt"
	"sort"
	"time"
)

func verifyOwnedGroup(ctx context.Context, inspector ProcessInspector, ownership Ownership) ([]ProcessSnapshot, error) {
	snapshots, err := inspector.ListGroup(ctx, ownership.ProcessGroupID)
	if err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return []ProcessSnapshot{}, nil
	}

	currentByPID := make(map[int]ProcessSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Identity.ProcessGroupID != ownership.ProcessGroupID {
			return nil, fmt.Errorf("%w: pid %d moved to process group %d", ErrOwnershipMismatch, snapshot.Identity.PID, snapshot.Identity.ProcessGroupID)
		}
		if _, duplicate := currentByPID[snapshot.Identity.PID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate pid %d", ErrOwnershipMismatch, snapshot.Identity.PID)
		}
		currentByPID[snapshot.Identity.PID] = snapshot
	}

	persistedByPID := make(map[int]ProcessIdentity, len(ownership.Members)+1)
	persistedByPID[ownership.Leader.PID] = ownership.Leader
	for _, member := range ownership.Members {
		persistedByPID[member.PID] = member
	}

	trusted := make(map[int]bool, len(snapshots))
	for pid, snapshot := range currentByPID {
		persisted, wasPersisted := persistedByPID[pid]
		if !wasPersisted {
			continue
		}
		if !sameProcessInstance(persisted, snapshot.Identity) {
			return nil, fmt.Errorf("%w: pid %d identity changed", ErrOwnershipMismatch, pid)
		}
		if sameProcessIdentity(persisted, snapshot.Identity) {
			trusted[pid] = true
		}
	}

	if leader, exists := currentByPID[ownership.Leader.PID]; exists {
		if !sameProcessIdentity(ownership.Leader, leader.Identity) {
			return nil, fmt.Errorf("%w: leader identity changed", ErrOwnershipMismatch)
		}
		trusted[ownership.Leader.PID] = true
	}

	for pid := range currentByPID {
		if trusted[pid] {
			continue
		}
		if !hasTrustedAncestor(pid, currentByPID, trusted) {
			return nil, fmt.Errorf("%w: pid %d is not a verified descendant", ErrOwnershipMismatch, pid)
		}
		trusted[pid] = true
	}

	sort.Slice(snapshots, func(left, right int) bool {
		return snapshots[left].Identity.PID < snapshots[right].Identity.PID
	})
	return snapshots, nil
}

func hasTrustedAncestor(pid int, current map[int]ProcessSnapshot, trusted map[int]bool) bool {
	visited := make(map[int]struct{})
	for {
		if _, seen := visited[pid]; seen {
			return false
		}
		visited[pid] = struct{}{}
		process, exists := current[pid]
		if !exists {
			return false
		}
		parentPID := process.Identity.ParentPID
		if trusted[parentPID] {
			return true
		}
		if _, parentInGroup := current[parentPID]; !parentInGroup {
			return false
		}
		pid = parentPID
	}
}

func sameProcessInstance(expected, actual ProcessIdentity) bool {
	return expected.PID == actual.PID &&
		expected.ProcessGroupID == actual.ProcessGroupID &&
		expected.StartedAt.Equal(actual.StartedAt)
}

func sameProcessIdentity(expected, actual ProcessIdentity) bool {
	return sameProcessInstance(expected, actual) &&
		expected.CommandFingerprint == actual.CommandFingerprint
}

func stableOwnedGroup(ctx context.Context, inspector ProcessInspector, ownership Ownership) ([]ProcessSnapshot, error) {
	first, err := verifyOwnedGroup(ctx, inspector, ownership)
	if err != nil {
		return nil, err
	}
	ownership.Members = identities(first)
	second, err := verifyOwnedGroup(ctx, inspector, ownership)
	if err != nil {
		return nil, err
	}
	if !sameSnapshotIdentities(first, second) {
		return nil, ErrUnstableGroup
	}
	return second, nil
}

func identities(snapshots []ProcessSnapshot) []ProcessIdentity {
	result := make([]ProcessIdentity, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, snapshot.Identity)
	}
	return result
}

func sameSnapshotIdentities(left, right []ProcessSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameProcessIdentity(left[index].Identity, right[index].Identity) ||
			left[index].Identity.ParentPID != right[index].Identity.ParentPID {
			return false
		}
	}
	return true
}

func observationFromSnapshots(path, state string, snapshots []ProcessSnapshot, observedAt time.Time) Observation {
	observation := Observation{
		OwnershipPath:     path,
		State:             state,
		OwnershipVerified: true,
		ObservedAt:        observedAt,
	}
	for _, snapshot := range snapshots {
		if snapshot.Status == "zombie" {
			continue
		}
		observation.MemberCount++
		observation.MemoryBytes += snapshot.MemoryBytes
		observation.CPUTime += snapshot.CPUTime
	}
	if observation.MemberCount == 0 && state == "running" {
		observation.State = "exited"
	}
	return observation
}

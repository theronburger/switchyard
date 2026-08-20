package processhost

import (
	"testing"
	"time"
)

// TestSameProcessInstanceRejectsUnknownStartTimes proves that an epoch or
// zero start time never satisfies an identity match: a zero kernel timestamp
// must not let two unrelated processes pass as the same instance.
func TestSameProcessInstanceRejectsUnknownStartTimes(t *testing.T) {
	epoch := time.Unix(0, 0)
	known := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	for _, startedAt := range []time.Time{{}, epoch, epoch.UTC()} {
		identity := ProcessIdentity{PID: 4242, ProcessGroupID: 4242, StartedAt: startedAt}
		if sameProcessInstance(identity, identity) {
			t.Fatalf("unknown start time %v matched itself", startedAt)
		}
	}
	identity := ProcessIdentity{PID: 4242, ProcessGroupID: 4242, StartedAt: known}
	if !sameProcessInstance(identity, identity) {
		t.Fatal("known start time did not match itself")
	}
}

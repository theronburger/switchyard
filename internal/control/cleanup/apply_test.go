package cleanup

import (
	"errors"
	"testing"
	"time"
)

func TestClaimTransitionsAreExplicitAndOrdered(t *testing.T) {
	claimedAt := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	claim, err := NewClaim("plan_01", 3, []string{"a", "b", "c"}, claimedAt)
	if err != nil || claim.Attempts != 1 || claim.Completed() {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if _, err := claim.Complete(claimedAt); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("completion without outcomes: %v", err)
	}
	if _, err := claim.Finish(Removal{CandidateID: "zzz", Reason: ReasonNotInPlan}); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("outcome for unrequested candidate: %v", err)
	}
	claim, err = claim.Begin("b")
	if err != nil || claim.InFlight != "b" {
		t.Fatalf("begin: %+v %v", claim, err)
	}
	if _, err := claim.Begin("a"); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("second in-flight candidate: %v", err)
	}
	if _, err := claim.Finish(Removal{CandidateID: "a", Removed: true}); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("finishing a candidate that is not in flight: %v", err)
	}
	// Resuming the in-flight candidate is the same transition again.
	if claim, err = claim.Begin("b"); err != nil {
		t.Fatal(err)
	}
	if claim, err = claim.Finish(Removal{CandidateID: "b", Reason: ReasonInterrupted}); err != nil || claim.InFlight != "" {
		t.Fatalf("finish: %+v %v", claim, err)
	}
	if _, err := claim.Begin("b"); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("re-beginning a final candidate: %v", err)
	}
	if _, err := claim.Finish(Removal{CandidateID: "a", Reason: "made-up"}); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("unknown reason: %v", err)
	}
	if claim, err = claim.Finish(Removal{CandidateID: "c", Reason: ReasonNotInPlan}); err != nil {
		t.Fatal(err)
	}
	if claim, err = claim.Retry(); err != nil || claim.Attempts != 2 || !claim.ClaimedAt.Equal(claimedAt) {
		t.Fatalf("retry: %+v %v", claim, err)
	}
	if _, err := claim.Complete(claimedAt.Add(-time.Second)); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("completion before claim: %v", err)
	}
	if _, err := claim.Result(); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("result before completion: %v", err)
	}
	if claim, err = claim.Begin("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := claim.Complete(claimedAt.Add(time.Second)); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("completion with an in-flight candidate: %v", err)
	}
	if claim, err = claim.Finish(Removal{CandidateID: "a", Removed: true}); err != nil {
		t.Fatal(err)
	}
	claim, err = claim.Complete(claimedAt.Add(time.Second))
	if err != nil || !claim.Completed() {
		t.Fatalf("complete: %+v %v", claim, err)
	}
	for _, transition := range []func() error{
		func() error { _, err := claim.Begin("a"); return err },
		func() error { _, err := claim.Finish(Removal{CandidateID: "a", Removed: true}); return err },
		func() error { _, err := claim.Retry(); return err },
		func() error { _, err := claim.Complete(claimedAt); return err },
	} {
		if err := transition(); !errors.Is(err, ErrClaimCompleted) {
			t.Fatalf("transition after completion: %v", err)
		}
	}
	result, err := claim.Result()
	if err != nil || result.Attempts != 2 || len(result.Removals) != 3 ||
		result.Removals[0] != (Removal{CandidateID: "a", Removed: true}) ||
		result.Removals[1] != (Removal{CandidateID: "b", Reason: ReasonInterrupted}) ||
		result.Removals[2] != (Removal{CandidateID: "c", Reason: ReasonNotInPlan}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestClaimMatchesOnlyTheExactRequest(t *testing.T) {
	claim, err := NewClaim("plan_01", 3, []string{"a", "b"}, time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"same":              claim.Matches("plan_01", 3, []string{"a", "b"}),
		"reordered":         claim.Matches("plan_01", 3, []string{"b", "a"}),
		"subset":            claim.Matches("plan_01", 3, []string{"a"}),
		"superset":          claim.Matches("plan_01", 3, []string{"a", "b", "c"}),
		"other revision":    claim.Matches("plan_01", 4, []string{"a", "b"}),
		"other plan":        claim.Matches("plan_02", 3, []string{"a", "b"}),
		"empty versus some": claim.Matches("plan_01", 3, []string{}),
	}
	for name, matched := range cases {
		if matched != (name == "same") {
			t.Fatalf("%s matched=%v", name, matched)
		}
	}
	if _, err := NewClaim("plan_01", 3, []string{"a", "a"}, time.Now()); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("duplicate candidate ids: %v", err)
	}
	if _, err := NewClaim("plan_01", 0, []string{}, time.Now()); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("unrevisioned plan: %v", err)
	}
	if _, err := NewClaim("plan_01", 1, nil, time.Now()); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("nil candidate list: %v", err)
	}
}

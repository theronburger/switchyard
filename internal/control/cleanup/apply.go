package cleanup

import (
	"errors"
	"time"
)

// Removal reasons are a finite vocabulary shared with the public contract.
const (
	ReasonNotInPlan          = "not-in-plan"
	ReasonChangedOrProtected = "changed-or-protected"
	// ReasonInterrupted records a candidate whose removal began under a claim
	// but cannot be proven complete: the daemon stopped, the request was
	// cancelled, or the filesystem failed part-way. The candidate is left
	// for a fresh plan to re-inventory; it is never reported as removed.
	ReasonInterrupted = "interrupted"
)

var (
	ErrClaimInvalid   = errors.New("cleanup claim is invalid")
	ErrClaimCompleted = errors.New("cleanup claim is already completed")
	ErrClaimMismatch  = errors.New("cleanup claim targets a different candidate set")
)

// Claim is the durable authorization for exactly one apply of one plan
// revision. It is created atomically before any owned resource is touched,
// records every candidate outcome as it becomes final, and names the single
// candidate whose removal is in flight so an interrupted apply is represented
// truthfully rather than as success or as an untouched plan.
//
// Transitions are value methods so the journal can persist every step:
//
//	claimed ──Begin(id)──▶ in flight ──Finish(removal)──▶ claimed ──Complete──▶ completed
type Claim struct {
	SchemaVersion int       `json:"schemaVersion"`
	PlanID        string    `json:"planId"`
	PlanRevision  int64     `json:"planRevision"`
	CandidateIDs  []string  `json:"candidateIds"`
	ClaimedAt     time.Time `json:"claimedAt"`
	Attempts      int       `json:"attempts"`
	InFlight      string    `json:"inFlight,omitempty"`
	Outcomes      []Removal `json:"outcomes"`
	CompletedAt   time.Time `json:"completedAt"`
}

func NewClaim(planID string, planRevision int64, candidateIDs []string, claimedAt time.Time) (Claim, error) {
	if candidateIDs == nil {
		return Claim{}, ErrClaimInvalid
	}
	claim := Claim{
		SchemaVersion: 1, PlanID: planID, PlanRevision: planRevision,
		CandidateIDs: append([]string{}, candidateIDs...), ClaimedAt: claimedAt.UTC(),
		Attempts: 1, Outcomes: []Removal{},
	}
	if err := claim.Validate(); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

func (claim Claim) Validate() error {
	if claim.SchemaVersion != 1 || claim.PlanID == "" || claim.PlanRevision < 1 || claim.CandidateIDs == nil ||
		claim.ClaimedAt.IsZero() || claim.Attempts < 1 || claim.Outcomes == nil {
		return ErrClaimInvalid
	}
	requested := make(map[string]struct{}, len(claim.CandidateIDs))
	for _, id := range claim.CandidateIDs {
		if id == "" {
			return ErrClaimInvalid
		}
		if _, duplicate := requested[id]; duplicate {
			return ErrClaimInvalid
		}
		requested[id] = struct{}{}
	}
	recorded := make(map[string]struct{}, len(claim.Outcomes))
	for _, outcome := range claim.Outcomes {
		if _, known := requested[outcome.CandidateID]; !known {
			return ErrClaimInvalid
		}
		if _, duplicate := recorded[outcome.CandidateID]; duplicate {
			return ErrClaimInvalid
		}
		if outcome.Removed && outcome.Reason != "" {
			return ErrClaimInvalid
		}
		if !outcome.Removed && outcome.Reason != ReasonNotInPlan && outcome.Reason != ReasonChangedOrProtected && outcome.Reason != ReasonInterrupted {
			return ErrClaimInvalid
		}
		recorded[outcome.CandidateID] = struct{}{}
	}
	if claim.InFlight != "" {
		if _, known := requested[claim.InFlight]; !known {
			return ErrClaimInvalid
		}
		if _, done := recorded[claim.InFlight]; done {
			return ErrClaimInvalid
		}
	}
	if !claim.CompletedAt.IsZero() && (claim.InFlight != "" || len(recorded) != len(requested) || claim.CompletedAt.Before(claim.ClaimedAt)) {
		return ErrClaimInvalid
	}
	return nil
}

// Matches reports whether a retry names exactly the claimed plan revision and
// candidate list, in order. Retry is deterministic only for the same request.
func (claim Claim) Matches(planID string, planRevision int64, candidateIDs []string) bool {
	if claim.PlanID != planID || claim.PlanRevision != planRevision || len(claim.CandidateIDs) != len(candidateIDs) {
		return false
	}
	for index, id := range candidateIDs {
		if claim.CandidateIDs[index] != id {
			return false
		}
	}
	return true
}

func (claim Claim) Completed() bool { return !claim.CompletedAt.IsZero() }

func (claim Claim) Outcome(candidateID string) (Removal, bool) {
	for _, outcome := range claim.Outcomes {
		if outcome.CandidateID == candidateID {
			return outcome, true
		}
	}
	return Removal{}, false
}

// Retry marks another attempt against the same claim. The claimed time and
// candidate list never change, so a resumed apply is the same transaction.
func (claim Claim) Retry() (Claim, error) {
	if claim.Completed() {
		return Claim{}, ErrClaimCompleted
	}
	claim.Attempts++
	return claim, nil
}

// Begin names the candidate about to be mutated. Re-beginning the candidate
// that was already in flight is how an interrupted attempt resumes.
func (claim Claim) Begin(candidateID string) (Claim, error) {
	if claim.Completed() {
		return Claim{}, ErrClaimCompleted
	}
	if _, done := claim.Outcome(candidateID); done || (claim.InFlight != "" && claim.InFlight != candidateID) {
		return Claim{}, ErrClaimInvalid
	}
	claim.InFlight = candidateID
	if err := claim.Validate(); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

// Finish records the final outcome for the in-flight candidate, or for a
// candidate that needed no mutation (for example one that is not in the plan).
func (claim Claim) Finish(removal Removal) (Claim, error) {
	if claim.Completed() {
		return Claim{}, ErrClaimCompleted
	}
	if claim.InFlight != "" && claim.InFlight != removal.CandidateID {
		return Claim{}, ErrClaimInvalid
	}
	claim.InFlight = ""
	claim.Outcomes = append(append([]Removal{}, claim.Outcomes...), removal)
	if err := claim.Validate(); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

// Complete closes the claim once every requested candidate has an outcome.
func (claim Claim) Complete(completedAt time.Time) (Claim, error) {
	if claim.Completed() {
		return Claim{}, ErrClaimCompleted
	}
	claim.CompletedAt = completedAt.UTC()
	if err := claim.Validate(); err != nil {
		return Claim{}, err
	}
	return claim, nil
}

// Result projects a completed claim in request order.
func (claim Claim) Result() (Result, error) {
	if !claim.Completed() {
		return Result{}, ErrClaimInvalid
	}
	removals := make([]Removal, 0, len(claim.CandidateIDs))
	for _, id := range claim.CandidateIDs {
		outcome, found := claim.Outcome(id)
		if !found {
			return Result{}, ErrClaimInvalid
		}
		removals = append(removals, outcome)
	}
	return Result{
		SchemaVersion: 1, PlanID: claim.PlanID, PlanRevision: claim.PlanRevision,
		Removals: removals, ClaimedAt: claim.ClaimedAt, Attempts: claim.Attempts, CompletedAt: claim.CompletedAt,
	}, nil
}

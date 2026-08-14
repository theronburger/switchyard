package health

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const CrashLoopAlertCode = "service_crash_loop"

var (
	ErrInvalidCrashLoop = errors.New("invalid crash-loop policy")
	ErrOutOfOrderEvent  = errors.New("health event is out of order")
)

type AlertTransition string

const (
	AlertUnchanged AlertTransition = "unchanged"
	AlertOpened    AlertTransition = "opened"
	AlertUpdated   AlertTransition = "updated"
	AlertResolved  AlertTransition = "resolved"
)

type CrashLoopPolicy struct {
	Window      time.Duration
	Threshold   int
	StableAfter time.Duration
}

type AlertObservation struct {
	AlertID     string          `json:"alertId"`
	Code        string          `json:"code"`
	Active      bool            `json:"active"`
	Transition  AlertTransition `json:"transition"`
	Occurrences int             `json:"occurrences"`
	FirstSeenAt time.Time       `json:"firstSeenAt,omitempty"`
	LastSeenAt  time.Time       `json:"lastSeenAt,omitempty"`
	ObservedAt  time.Time       `json:"observedAt"`
}

type CrashTracker struct {
	mutex       sync.Mutex
	alertID     string
	policy      CrashLoopPolicy
	exits       []time.Time
	active      bool
	firstSeenAt time.Time
	lastSeenAt  time.Time
	lastEventAt time.Time
}

func NewCrashTracker(alertID string, policy CrashLoopPolicy) (*CrashTracker, error) {
	if alertID == "" || policy.Window <= 0 || policy.Threshold < 2 || policy.StableAfter <= 0 {
		return nil, ErrInvalidCrashLoop
	}
	return &CrashTracker{alertID: alertID, policy: policy}, nil
}

func (tracker *CrashTracker) RecordExit(at time.Time) (AlertObservation, error) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if at.IsZero() || (!tracker.lastEventAt.IsZero() && at.Before(tracker.lastEventAt)) {
		return AlertObservation{}, ErrOutOfOrderEvent
	}
	tracker.lastEventAt = at
	tracker.prune(at)
	tracker.exits = append(tracker.exits, at)
	transition := AlertUnchanged
	if len(tracker.exits) >= tracker.policy.Threshold {
		if !tracker.active {
			tracker.active = true
			tracker.firstSeenAt = at
			transition = AlertOpened
		} else {
			transition = AlertUpdated
		}
		tracker.lastSeenAt = at
	}
	return tracker.observation(at, transition), nil
}

func (tracker *CrashTracker) Observe(at, runningSince time.Time) (AlertObservation, error) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if at.IsZero() || (!tracker.lastEventAt.IsZero() && at.Before(tracker.lastEventAt)) {
		return AlertObservation{}, ErrOutOfOrderEvent
	}
	tracker.lastEventAt = at
	tracker.prune(at)
	transition := AlertUnchanged
	if tracker.active && !runningSince.IsZero() && !runningSince.Before(tracker.lastSeenAt) && at.Sub(runningSince) >= tracker.policy.StableAfter {
		tracker.active = false
		transition = AlertResolved
		observation := tracker.observation(at, transition)
		tracker.exits = nil
		tracker.firstSeenAt = time.Time{}
		tracker.lastSeenAt = time.Time{}
		return observation, nil
	}
	return tracker.observation(at, transition), nil
}

func (tracker *CrashTracker) prune(at time.Time) {
	cutoff := at.Add(-tracker.policy.Window)
	first := 0
	for first < len(tracker.exits) && tracker.exits[first].Before(cutoff) {
		first++
	}
	if first > 0 {
		tracker.exits = append([]time.Time(nil), tracker.exits[first:]...)
	}
}

func (tracker *CrashTracker) observation(at time.Time, transition AlertTransition) AlertObservation {
	return AlertObservation{
		AlertID:     tracker.alertID,
		Code:        CrashLoopAlertCode,
		Active:      tracker.active,
		Transition:  transition,
		Occurrences: len(tracker.exits),
		FirstSeenAt: tracker.firstSeenAt,
		LastSeenAt:  tracker.lastSeenAt,
		ObservedAt:  at,
	}
}

func (observation AlertObservation) String() string {
	return fmt.Sprintf("%s active=%t occurrences=%d transition=%s", observation.Code, observation.Active, observation.Occurrences, observation.Transition)
}

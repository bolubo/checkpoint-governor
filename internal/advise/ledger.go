package advise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// ledgerName is the private file: the implementation's memory, not the contract.
const ledgerName = "internal.json"

// sampleWindow is how many recent measurements are kept.
//
// The policy uses the 90th percentile of the write costs, so the window has to be long enough for a
// bad day to appear in it and short enough that a storage change is visible. Sixteen is a starting
// point; the replay in phase 2 is what should move it.
const sampleWindow = 16

// Ledger survives a controller restart. Everything the controller remembers between rounds lives
// here, so that a decision is a function of the snapshot and this file — never of process memory.
type Ledger struct {
	Schema            int              `json:"schema"`
	Epoch             int64            `json:"epoch"`
	UnitStartedAt     int64            `json:"unitStartedAt"`
	WriteSecondsTotal float64          `json:"writeSecondsTotal"`
	FirstSeen         map[string]int64 `json:"firstSeen"`
	BootID            string           `json:"bootId"`
	Generation        int64            `json:"generation"`
	Failures          int              `json:"failures"`
	WriteCosts        []float64        `json:"writeCosts"`
	StepTimes         []float64        `json:"stepTimes"`

	// LastSealStep and LastFailedDir are how the loop counts each event once: a seal already
	// recorded must not be recorded again, and a broken directory must not raise the failure count
	// on every round.
	LastSealStep  int64  `json:"lastSealStep,omitempty"`
	LastFailedDir string `json:"lastFailedDir,omitempty"`
	// HasSeal separates "no seal yet" from "a seal at step zero": a job that starts at step 0 is
	// normal, and treating its first checkpoint as "already recorded" loses it.
	HasSeal bool `json:"hasSeal,omitempty"`
	// CauseWrittenID is the event this unit last wrote for, and CauseWrittenAt when it did (0022). It
	// is what makes a preemption cost one write instead of one per round: the same waiting workload
	// carries the same id, so the next round knows the event has already been paid for.
	CauseWrittenID string `json:"causeWrittenId,omitempty"`
	CauseWrittenAt int64  `json:"causeWrittenAt,omitempty"`

	// Reconciled counts restarts that found a published plan the ledger had not recorded yet. It is
	// a diagnostic: a persistent non-zero value means saves are being lost.
	Reconciled int `json:"reconciled,omitempty"`

	// Verification budget: how many bytes were read back in the current hour. Reading a checkpoint
	// costs more than writing it, so it is the one measurement that has to be rationed.
	VerifyWindowStart int64 `json:"verifyWindowStart,omitempty"`
	VerifyBytesWindow int64 `json:"verifyBytesWindow,omitempty"`

	// PublishTimes are the moments the plan was written recently, in unix seconds. They exist to stop
	// a flapping plan from turning into a storm of fsyncs: the file keeps the last plan, which is at
	// least consistent, instead of being rewritten several times a second.
	PublishTimes []int64 `json:"publishTimes,omitempty"`

	// Decision cadence. Observation is cheap and frequent; a decision is free but a new plan can
	// rewrite the file, so the loop calls the policy only when the answer can have changed.
	NextDecisionAt  int64           `json:"nextDecisionAt,omitempty"`
	DecidedSealStep int64           `json:"decidedSealStep,omitempty"`
	DecidedFailures int             `json:"decidedFailures,omitempty"`
	WasInFlight     bool            `json:"wasInFlight,omitempty"`
	LastDecision    policy.Decision `json:"lastDecision,omitempty"`

	// AbsentAt is the last moment a step directory was seen **without** a manifest. With the first
	// sighting it brackets when the seal happened, which is what turns a write cost from "one
	// observation" into an interval (see design, "Measurement").
	AbsentAt map[string]int64 `json:"absentAt,omitempty"`

	// Verified and Mismatch are the sampled verifier's results, by step. They persist because
	// reading a checkpoint back costs more than writing it: a restart must not start over.
	Verified     map[int64]int64  `json:"verified,omitempty"`
	Mismatch     map[int64]string `json:"mismatch,omitempty"`
	LastVerifyAt int64            `json:"lastVerifyAt,omitempty"`
	// Status is the human view (design, "The human view"). It lives here because it is the
	// implementation's memory, not the contract's.
	Status Status `json:"status"`
}

// Load reads the ledger, returning an empty one when there is none yet.
func Load(dir string) (Ledger, error) {
	l := Ledger{
		Schema:    SchemaVersion,
		FirstSeen: map[string]int64{},
		AbsentAt:  map[string]int64{},
		Verified:  map[int64]int64{},
		Mismatch:  map[int64]string{},
	}
	raw, err := os.ReadFile(filepath.Join(dir, BaseDir, ledgerName))
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, err
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		// A corrupt ledger is not a reason to stop governing writes; it is a reason to forget.
		return Ledger{Schema: SchemaVersion, FirstSeen: map[string]int64{}}, nil
	}
	if l.FirstSeen == nil {
		l.FirstSeen = map[string]int64{}
	}
	if l.AbsentAt == nil {
		l.AbsentAt = map[string]int64{}
	}
	if l.Verified == nil {
		l.Verified = map[int64]int64{}
	}
	if l.Mismatch == nil {
		l.Mismatch = map[int64]string{}
	}
	return l, nil
}

// VerifiedSteps and MismatchedSteps are the observer's inputs: a copy the verifier read is verified,
// and a copy it found wrong is broken.
func (l *Ledger) VerifiedSteps() map[int64]bool {
	out := make(map[int64]bool, len(l.Verified))
	for step := range l.Verified {
		out[step] = true
	}
	return out
}

func (l *Ledger) MismatchedSteps() map[int64]bool {
	out := make(map[int64]bool, len(l.Mismatch))
	for step := range l.Mismatch {
		out[step] = true
	}
	return out
}

// Save writes the ledger atomically.
func (l *Ledger) Save(dir string) error {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, BaseDir, ledgerName), raw)
}

// Observe records the first moment a path was seen and reports whether it was new.
//
// Only the first sighting is kept: a write's cost is "seal seen minus first seen", and updating the
// start on every round would make every write look instantaneous.
func (l *Ledger) Observe(path string, at time.Time) bool {
	if l.FirstSeen == nil {
		l.FirstSeen = map[string]int64{}
	}
	if _, ok := l.FirstSeen[path]; ok {
		return false
	}
	l.FirstSeen[path] = at.UnixNano()
	return true
}

// Seen returns the first sightings as times, for the observer's Request.
func (l *Ledger) Seen() map[string]time.Time {
	out := make(map[string]time.Time, len(l.FirstSeen))
	for path, ns := range l.FirstSeen {
		out[path] = time.Unix(0, ns)
	}
	return out
}

// AddWriteCost appends one completed write and keeps the window bounded.
func (l *Ledger) AddWriteCost(seconds float64) {
	l.WriteCosts = appendSample(l.WriteCosts, seconds)
}

// AddStepTime appends one step-time measurement and keeps the window bounded.
func (l *Ledger) AddStepTime(seconds float64) {
	l.StepTimes = appendSample(l.StepTimes, seconds)
}

func appendSample(values []float64, v float64) []float64 {
	values = append(values, v)
	if len(values) > sampleWindow {
		values = values[len(values)-sampleWindow:]
	}
	return values
}

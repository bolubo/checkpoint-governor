// Package produce turns the environment into the conditions the algorithm reads.
//
// Every condition is one `Probe`. A probe owns its raw signal, its thresholds and its "unreadable
// after N"; the algorithm owns none of them (decision 0013). A probe answers with exactly one of
// three things, and the difference between the first two is the whole point:
//
//	Absent  — this environment does not have this condition (a CPU job and a GPU fault). Inert.
//	Unknown — it has it, but we cannot read it right now. Blindness, after N failures.
//	Fresh   — 0 or 1, read just now.
package produce

import (
	"errors"
	"time"
)

// ErrProbeBroken is the sentinel a probe wraps when it is the probe that is dead, not the condition
// it was trying to read. A missing file is "cannot read this condition now"; a probe that panics on
// itself is "the watchdog is gone", and the two deserve different answers (0013: watch.broken).
var ErrProbeBroken = errors.New("probe is dead")

// State is one of the three answers.
type State int

const (
	// Absent means the environment has no such condition.
	Absent State = iota
	// Unknown means the condition exists but could not be read.
	Unknown
	// Fresh means Value is a current reading.
	Fresh
)

func (s State) String() string {
	switch s {
	case Absent:
		return "absent"
	case Fresh:
		return "fresh"
	default:
		return "unknown"
	}
}

// Reading is one probe's answer at one moment.
type Reading struct {
	// Condition is the frozen name from the catalogue (decision 0013).
	Condition string
	State     State
	// Value is meaningful only when State is Fresh.
	Value bool
	// At is when the reading was taken; zero for Absent.
	At time.Time
	// Err is why the most recent read failed, when it did. A Fresh reading with a non-nil Err is the
	// last good answer carried through a failed read: the error is for the log, never for the
	// algorithm.
	Err error
	// Misses is how many consecutive reads failed behind this answer. A Fresh reading with misses
	// above zero is the last good answer, carried forward while the sampler stays inside its
	// failure budget: it was true at At, and nobody claims it is true now.
	Misses int
}

// Cost is the price of one read, which decides how often a probe may run.
type Cost int

const (
	// Cheap is one syscall or a file read.
	Cheap Cost = iota
	// Heavy reads checkpoint content or walks a tree.
	Heavy
)

// Env is what a probe is given. It must not read a clock of its own.
type Env struct {
	Dir string
	Now time.Time
	// CheckpointBytes is the size of the newest complete checkpoint, which the storage condition
	// needs for its 200% rule. Zero means unknown.
	CheckpointBytes int64
}

// Probe is one condition.
type Probe interface {
	// Name is the frozen catalogue name.
	Name() string
	// Read answers with exactly one of absent, unknown or a fresh boolean.
	Read(Env) Reading
	// Every is how often this probe must be read.
	Every() time.Duration
	// Cost says whether reading it competes with training I/O.
	Cost() Cost
}

package policy

import (
	"fmt"
	"time"
)

// riskVersion is the policy that replaced the interval search: it watches the condition catalogue
// and advises a write when the work may be lost. It has no clock, no threshold, and no arithmetic.
const riskVersion = "risk-v1"

// Risk is the risk policy (docs/decisions/0015-risk-core.md).
//
// Extra holds the conditions a deployment added from disk (decision 0024). They are appended after
// the frozen catalogue, so when two urgent conditions fire the decision still names the built-in one:
// a condition this repository owns is more certain than one an operator wrote, and precedence is
// consequence, not file order. The zero value is the built-in catalogue, which is what every caller
// without a conditions.d directory gets.
type Risk struct {
	Extra []Condition
}

func (Risk) Name() string { return riskVersion }

// catalogue is the rows this policy watches: the frozen set, then the deployment's own.
func (r Risk) catalogue() []Condition {
	if len(r.Extra) == 0 {
		return conditionCatalog
	}
	rows := make([]Condition, 0, len(conditionCatalog)+len(r.Extra))
	rows = append(rows, conditionCatalog...)
	return append(rows, r.Extra...)
}

// reading is what one look at one condition produced.
type reading int

const (
	// readingAbsent: the environment has no such thing (a CPU job has no GPU). Inert.
	readingAbsent reading = iota
	// readingUnknown: the environment has it, but we cannot read it right now.
	readingUnknown
	// readingTrue, readingFalse: a fresh boolean.
	readingTrue
	readingFalse
)

// Decide returns the decision for one save point.
//
// The order is the rule: the configuration gate first, then the job-local facts that cannot wait
// for a condition, then the catalogue in its precedence order. A condition that is true decides
// nothing by itself; what it means is in its tier.
func (r Risk) Decide(b Bounds, s Snapshot) Decision {
	d := Decision{
		UnitID:        s.UnitID,
		Generation:    s.Generation,
		Version:       riskVersion,
		Action:        ActionSkip,
		Reason:        ReasonCalm,
		Kind:          WriteFull,
		KeepCopies:    maxInt(1, b.MinCopies),
		CopiesToWrite: maxInt(1, b.MinCopies),
	}
	if b.NoUs == NoUsEvery {
		d.BaselineSeconds = int64(b.NoUsEvery.Round(time.Second).Seconds())
	}
	if b.NoUs == NoUsNone {
		d.BaselineNone = true
	}
	if b.WithUs > 0 {
		d.WindowSeconds = int64(b.WithUs.Round(time.Second).Seconds())
	}

	// The configuration gate: an executor must not start with an unanswered `no-us`. "Not answered"
	// is not "no periodic writes"; only the user can say that, and the saving is computed from it.
	if b.NoUs == NoUsUnset {
		d.Action = ActionUnknown
		d.Reason = ReasonUnknown
		d.Missing = []string{"no-us"}
		d.Why = "the user has not answered no-us: what would the job do if the governor were gone? Answer none, or a duration"
		return d
	}

	// Safety facts first: a running or repeatedly failing write is bigger than any condition.
	if s.ConsecutiveWriteFailures >= 2 {
		d.Action = ActionInfeasible
		d.Reason = ReasonRepair
		d.Why = fmt.Sprintf("the last %d writes failed; a person has to look at the storage", s.ConsecutiveWriteFailures)
		d.Reasons = []ReasonEntry{reason(codeRepairStorageFailing,
			map[string]float64{"failures": float64(s.ConsecutiveWriteFailures)},
			"ConsecutiveWriteFailures")}
		return d
	}
	if newest := newestCopy(s.Copies); newest != nil && newest.State == CopyBroken {
		d.Action = ActionRepair
		d.Reason = ReasonRepair
		d.SaveNow = true
		d.Why = fmt.Sprintf("checkpoint at step %d is broken; write it again now", newest.Step)
		d.Reasons = []ReasonEntry{reason(codeRepairBrokenCopy,
			map[string]float64{"step": float64(newest.Step)}, "Copies")}
		return d
	}
	if s.SaveInFlight {
		d.Action = ActionHold
		d.Reason = ReasonSchedule
		d.Why = "a write is already running"
		d.Reasons = []ReasonEntry{reason(codeWriteInFlight,
			map[string]float64{"progress": s.SaveProgress}, "SaveInFlight", "SaveProgress")}
		return d
	}

	// The catalogue, in precedence order.
	var watched []ReasonEntry
	for _, c := range r.catalogue() {
		switch read(s, c.Name) {
		case readingTrue:
			if c.Tier == TierUrgent {
				d.Action = ActionSave
				d.Reason = ReasonRisk
				d.SaveNow = true
				d.Why = fmt.Sprintf("%s: %s", c.Name, c.Why)
				d.Reasons = append([]ReasonEntry{reason(c.Name, map[string]float64{}, "Evidence["+c.Name+"]")}, watched...)
				return d
			}
			watched = append(watched, reason(c.Name, map[string]float64{}, "Evidence["+c.Name+"]"))
		case readingUnknown:
			if c.Missing == MissingUrgent {
				d.Action = ActionSave
				d.Reason = ReasonRisk
				d.SaveNow = true
				d.Missing = append(d.Missing, "blind:"+c.Name)
				d.Why = fmt.Sprintf("cannot read %s: %s", c.Name, c.Why)
				d.Reasons = append([]ReasonEntry{reason(c.Name, map[string]float64{}, "Evidence["+c.Name+"]")}, watched...)
				return d
			}
		}
	}

	// Nothing fired: no write. The reasons list what is being watched, so a user can see that the
	// silence is a decision and not an absence of watching.
	d.Reasons = watched
	d.Why = "no condition says the work is at risk"
	return d
}

// read looks at one condition. A value of the wrong kind is treated as unreadable, never as false:
// the fail-closed direction is what keeps a broken producer from looking calm.
func read(s Snapshot, name string) reading {
	o, ok := s.Evidence[name]
	if !ok {
		return readingAbsent
	}
	if o.Kind != Boolean {
		return readingUnknown
	}
	if o.Bool {
		return readingTrue
	}
	return readingFalse
}

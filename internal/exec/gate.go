package exec

import (
	"fmt"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Verdict is what the gate says about starting a write.
type Verdict string

const (
	// Start means the executor may begin an attempt now.
	Start Verdict = "start"
	// Hold means a write is already in flight; wait for it.
	Hold Verdict = "hold"
	// Refuse means this decision must not start a write, and Reason says why.
	Refuse Verdict = "refuse"
)

// Result is the gate's answer.
type Result struct {
	Verdict Verdict
	// Reason is a short code, safe to print and to log: "not-a-save", "in-flight",
	// "headroom", "duplicate", "cooldown", "backoff", "volume-busy", "no-time".
	Reason string
	// Wait is how long until Start would be allowed; zero when it is allowed now.
	Wait time.Duration
	// Warning is a rule the gate could not apply, and still allowed the write. An unknown is not a
	// zero: with no checkpoint size yet there is no scale for the headroom rule, and passing silently
	// would make a person believe the first write was guarded when it was not.
	Warning string
	// Detail is the sentence a person reads when the reason alone is not enough: the sizes behind a
	// headroom refusal, or the deadline arithmetic behind a no-time one.
	Detail string
}

// Env is what the gate needs that is neither in the decision nor in the attempts: the step the
// request claims, the size of the newest checkpoint, the free space, how many copies the write has
// to leave room for, and the two clocks only the executor may see. It is passed in, not read, so the
// gate stays a pure function (0016 R1).
type Env struct {
	Step            int64
	CheckpointBytes int64
	FreeBytes       int64
	// Retained is how many complete copies must survive the next write: the ones retention keeps.
	// Zero means there is nothing to coexist with, which is the cold start.
	Retained int
	// Copies is how many copies the next write itself produces (the decision's CopiesToWrite).
	// Zero or one means one copy; a second storage root is backlog, so in practice it is one.
	Copies int
	// SealedStep is the newest step that already has a complete copy. A request for that step or
	// something older is already satisfied: asking the job to write what exists is a write nobody
	// needs, and counting it would put a "sealed" in the history for a write that never happened.
	SealedStep int64
	// SecondsLeft is how long we have before we are killed, when something announced a deadline.
	// Zero or negative means nobody announced one.
	SecondsLeft float64
	// HaveSeconds separates "the job said zero" from "the job said nothing": only the second one is a
	// gap. (Glue's reader reports both, and a zero window is a real announcement.)
	HaveSeconds bool
	// WriteSeconds is how long one write is expected to take. Zero or negative means unknown.
	WriteSeconds float64
	// Teardown is how long the job itself needs after the write; zero means the default margin.
	Teardown time.Duration
	// Final says this request is the last chance the work gets: the job is ending, and there is no
	// next round in which to ask again.
	Final bool
	// Permit is the fleet's verdict for this unit (0021). The zero value is granted: a single-unit
	// deployment has no fleet, and an unreadable verdict arrives here as permission.
	Permit permit.Verdict
	// PermitUntil bounds a Queued verdict. Zero means unbounded, which is read as granted rather than
	// as a queue that never ends.
	PermitUntil time.Time

	// CauseID names the event endangering this unit: the set of higher-priority work waiting for room,
	// as the observer read it (0022). Empty means it cannot be read, which must never suppress a write.
	CauseID string
	// CauseHandled says this unit has already written for that event.
	CauseHandled bool
}

// TeardownMargin is how much of the announced window the job's own shutdown is assumed to need. A
// write that just barely fits is a write that makes the job miss its kill: the platform's clock does
// not wait for our fsync.
const TeardownMargin = 10 * time.Second

// margin is the teardown this gate uses: the job's own number when it gave one, the default otherwise.
func (e Env) margin() time.Duration {
	if e.Teardown > 0 {
		return e.Teardown
	}
	return TeardownMargin
}

// Pressured is how many checkpoints the free space has to hold: the new copies plus the ones that
// stay. Cold start is one copy, nothing to coexist with; steady state is one plus what retention
// keeps. The old constant "2×" was only right for exactly one retained copy.
func (e Env) Pressured() int {
	n := e.Copies
	if n < 1 {
		n = 1
	}
	if e.Retained < 0 {
		return n
	}
	return n + e.Retained
}

// Gate decides whether a decision may start a write now.
//
// It is the only place that owns cooldown, backoff, deduplication and the 200% headroom rule
// (decision 0016). It is a pure function of the decision, the recent attempts, the environment and
// the clock: no I/O, no state, so it can be tested as a truth table.
//
// The doors are checked in this order, because the earlier one is the more specific truth:
//
//	not-a-save   the decision does not ask for a write
//	not-the-target  the fleet selected another unit for this cause (0021); a second write saves nothing
//	in-flight    a write is already running; starting another cannot help
//	already-written-for-this-risk  this unit already wrote for this event (0022)
//	already-saved  the step the request names is already on disk
//	no-time      the job's own deadline is shorter than one write; the write would be killed halfway
//	headroom     free space cannot hold the new copies and the ones that stay
//	duplicate    this exact request is already in the history
//	cooldown     the last write ended too recently
//	backoff      this condition has been failing; wait longer each time
func Gate(d policy.Decision, recent []Attempt, env Env, now time.Time) Result {
	if d.Action != policy.ActionSave {
		return Result{Verdict: Refuse, Reason: "not-a-save"}
	}
	// The fleet door: when one cause endangers several units, only the ones the cause selects may
	// spend a write (0021). A unit that sees only itself cannot tell "I am the victim" from "my
	// neighbour is", so the verdict comes from the component that sees the set — and every failure of
	// that channel arrives here as granted, because coordination must never be able to suppress a save.
	switch env.Permit {
	case permit.Held:
		return Result{Verdict: Refuse, Reason: "not-the-target",
			Detail: "another unit is the one this cause selects; a second write would pay for one death"}
	case permit.Queued:
		if !env.PermitUntil.IsZero() && now.Before(env.PermitUntil) {
			return Result{Verdict: Hold, Reason: "queued", Wait: env.PermitUntil.Sub(now)}
		}
	}
	for _, a := range recent {
		if !a.Done() {
			return Result{Verdict: Hold, Reason: "in-flight"}
		}
	}
	// One decision, one write, per risk event. A preemption is an event — a set of higher-priority
	// work waiting for room — and it is decided once: either nobody dies (nothing to write) or a
	// chosen unit or batch writes once. While the same event is in force nothing new is written; the
	// window is what keeps the checkpoint fresh afterwards (0017).
	//
	// The identity is the cause the observer read, not a timer. Two rounds of the same waiting
	// workload are the same thing happening; a different waiting workload is a new event. An unreadable
	// cause has no identity to compare, and refusing on that would suppress the write the risk exists
	// to justify — so an empty id always writes.
	if env.CauseHandled && env.CauseID != "" {
		return Result{Verdict: Refuse, Reason: "already-written-for-this-risk",
			Detail: fmt.Sprintf("this unit already wrote for risk %s; the window keeps it fresh from here", env.CauseID)}
	}
	// Already saved: the step the request names has a complete copy. This is the truth a stop request
	// runs into most often — "save what I have" when what it has is already on disk.
	if env.Step > 0 && env.SealedStep >= env.Step {
		return Result{Verdict: Refuse, Reason: "already-saved"}
	}

	// The deadline door: something announced "you will be killed in N seconds". If the write cannot
	// finish with a margin for the job's own teardown, starting it spends the shutdown window on a
	// checkpoint that will never be sealed. Refusing is the honest answer, and the refusal names the
	// arithmetic so a person can see what it costs.
	//
	// An unknown write cost does not refuse: a refusal on ignorance would guarantee the loss the
	// window was the last chance to avoid. It writes and says that the check was skipped.
	warnings := []string{}
	if env.HaveSeconds || env.SecondsLeft > 0 {
		switch {
		case env.WriteSeconds <= 0:
			warnings = append(warnings, "no-time check skipped: write cost unknown")
		case env.WriteSeconds+env.margin().Seconds() > env.SecondsLeft:
			return Result{Verdict: Refuse, Reason: "no-time", Detail: FormatNoTime(env)}
		}
	}

	// The space rule: the new copies and the copies that stay have to coexist until the new ones are
	// sealed and verified, so a write into less than (copies + retained) checkpoints is the write that
	// fills the disk. With no size there is no scale at all, and the rule is not applied — out loud.
	switch {
	case env.CheckpointBytes <= 0:
		warnings = append(warnings, "headroom unknown (no checkpoint yet)")
	case env.FreeBytes < 0:
		warnings = append(warnings, "headroom unknown (free space not measured)")
	default:
		if need := int64(env.Pressured()) * env.CheckpointBytes; env.FreeBytes < need {
			return Result{Verdict: Refuse, Reason: "headroom", Detail: FormatHeadroom(env)}
		}
	}

	trigger, reason := TriggerOf(d)
	id := identity(d.UnitID, d.Generation, env.Step, trigger, reason)
	for _, a := range recent {
		// A refusal is not a served request: it wrote nothing, so the same request may be asked again
		// (and refused again) instead of being deduplicated into silence forever.
		if a.ID == id && (!a.Done() || attempted(a)) {
			return Result{Verdict: Refuse, Reason: "duplicate"}
		}
	}

	// The two throttles below exist to protect the *next* rounds: "do not write twice in a second",
	// "do not hammer a condition that keeps failing". A final request has no next round — the work
	// that exists now is the work that matters — so they are skipped, and the run is told nothing
	// was refused. Every other door still applies: no space and no time are facts, not throttles.
	if !env.Final {
		last, ok := lastFinished(recent)
		cost := 0.0
		if ok {
			cost = last.Cost()
			if wait := Cooldown(cost) - now.Sub(last.EndedAt); wait > 0 {
				return Result{Verdict: Refuse, Reason: "cooldown", Wait: wait}
			}
		}

		if n := trailingFailures(recent, reason); n > 0 {
			if wait := Backoff(cost, n) - now.Sub(lastEnded(recent, reason)); wait > 0 {
				return Result{Verdict: Refuse, Reason: "backoff", Wait: wait}
			}
		}
	}
	return Result{Verdict: Start, Warning: strings.Join(warnings, "; ")}
}

// ReasonWindow names the attempt the user's window produced. It is not a policy code: no condition
// asked for this write, the span did. It is spelled here because the executor is the only side that
// creates it, and the history has to say so.
const ReasonWindow = "window"

// TriggerOf maps a decision to the attempt's trigger and the fact it names. The history is grouped
// by trigger; the reason is the condition (or repair case) a reader sees.
func TriggerOf(d policy.Decision) (Trigger, string) {
	if d.Action == policy.ActionRepair {
		return TriggerRepair, firstCode(d)
	}
	if c := firstCode(d); c == ReasonWindow {
		return TriggerWindow, c
	}
	if d.Reason == policy.ReasonFirst {
		return TriggerFirst, string(d.Reason)
	}
	return TriggerCondition, firstCode(d)
}

func firstCode(d policy.Decision) string {
	if len(d.Reasons) > 0 {
		return d.Reasons[0].Code
	}
	return string(d.Reason)
}

// Cost is how long the write took. A verdict that did not finish a write has no duration: reporting
// the wait-until-deadline as a write time would make the cooldown three times the deadline, and that
// is how a gate ends up refusing a volume that is idle.
func (a Attempt) Cost() float64 {
	if a.StartedAt.IsZero() || a.EndedAt.IsZero() {
		return 0
	}
	switch a.Outcome {
	case Sealed, Broken:
		// The write finished (a broken copy is a finished write with bad content).
		return a.EndedAt.Sub(a.StartedAt).Seconds()
	}
	return 0
}

// lastFinished returns the attempt that ended most recently, counting only the ones that actually
// asked the job for something. A refusal is not a write: it produced nothing, so it must not reset
// the cooldown or start a backoff, or "we said no" would become the reason we keep saying no.
func lastFinished(attempts []Attempt) (Attempt, bool) {
	var last Attempt
	found := false
	for _, a := range attempts {
		if !attempted(a) || a.EndedAt.IsZero() {
			continue
		}
		if !found || a.EndedAt.After(last.EndedAt) {
			last, found = a, true
		}
	}
	return last, found
}

// attempted says whether this outcome means the job was actually asked. Sealed, broken, abandoned
// and failed all mean a write was requested and something (or nothing) came of it.
func attempted(a Attempt) bool {
	switch a.Outcome {
	case Sealed, Broken, Abandoned, Failed:
		return true
	}
	return false
}

// failedOutcome is the subset of attempted outcomes that means the write did not produce a usable
// checkpoint. Only those grow the backoff: a refusal is a decision we made, not a failure.
func failedOutcome(o Outcome) bool {
	return o == Failed || o == Abandoned || o == Broken
}

// trailingFailures counts how many of this condition's attempts ended without a seal since the last
// successful write. A seal resets the count: the condition is not stuck: it had one bad write.
func trailingFailures(attempts []Attempt, reason string) int {
	n := 0
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Outcome == Sealed {
			break
		}
		if a.Reason != reason || !failedOutcome(a.Outcome) {
			continue
		}
		n++
	}
	return n
}

// lastEnded is when the most recent attempt for this reason ended, zero when there is none.
func lastEnded(attempts []Attempt, reason string) time.Time {
	var last time.Time
	for _, a := range attempts {
		if a.Reason == reason && attempted(a) && a.EndedAt.After(last) {
			last = a.EndedAt
		}
	}
	return last
}

// FormatNoTime prints the deadline verdict the way a person reads it:
// "no-time (need 4m10s, have 30s)". The price (the work since the last checkpoint) is added by the
// caller, which is the only side that can read the directory.
func FormatNoTime(env Env) string {
	return fmt.Sprintf("no-time (need %s, have %s)",
		shortSeconds(env.WriteSeconds+env.margin().Seconds()), shortSeconds(env.SecondsLeft))
}

// shortSeconds formats a number of seconds the way a person says it.
func shortSeconds(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// FormatHeadroom prints the space verdict the way a person reads it:
// "headroom 1.2x, need 3.0x (1 new + 2 retained)".
func FormatHeadroom(env Env) string {
	if env.CheckpointBytes <= 0 {
		return "headroom unknown (no checkpoint yet)"
	}
	if env.FreeBytes < 0 {
		return "headroom unknown (free space not measured)"
	}
	have := float64(env.FreeBytes) / float64(env.CheckpointBytes)
	return fmt.Sprintf("headroom %.1fx, need %d.0x (%d new + %d retained)",
		have, env.Pressured(), env.Pressured()-env.Retained, env.Retained)
}

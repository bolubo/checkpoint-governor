// Package loop is one round of the controller: read the ledger, read the directory, decide,
// publish, remember.
//
// It is the only place where the three layers meet. Everything it does is one of: measuring (via
// observe), deciding (via policy) or delivering (via advise), plus the bookkeeping that makes the
// next round mean the same thing as this one.
package loop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/exec"
	"github.com/bolubo/checkpoint-governor/internal/lease"
	"github.com/bolubo/checkpoint-governor/internal/notify"
	"github.com/bolubo/checkpoint-governor/internal/observe"
	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
)

// Config is one governed directory.
type Config struct {
	Dir    string
	UnitID string
	Bounds policy.Bounds
	Policy policy.Policy
	Now    func() time.Time

	// Permit answers "may this unit spend a write" when one cause endangers several units (0021).
	// Nil means yes: a deployment with no observer has no fleet to coordinate with, and every failure
	// of the channel must read as permission.
	Permit func(now time.Time) (permit.Verdict, time.Time)

	// CauseID names the event endangering this unit — the waiting higher-priority work, as the
	// observer read it (0022). Nil or empty means the deployment cannot read it, and the gate then
	// writes as it always did: an unreadable event must never suppress a save.
	CauseID func(now time.Time) string

	// VerifyEvery is how often one checkpoint may be read back. Zero means ten minutes; a negative
	// value turns verification off. Reading a checkpoint costs more than writing it, so the default
	// is deliberately rare.
	VerifyEvery time.Duration

	// Fault is the crash-injection hook: a test returns an error at a named point and the round stops
	// there, as a killed process would. The points are "publish", "record", "seal", "retire" and
	// "sweep". Production leaves it nil.
	Fault func(point string) error

	// MaxPublishPerMinute breaks the circuit when the plan flaps. Zero means twelve; a negative
	// value means no limit (only for tests).
	MaxPublishPerMinute int

	// VerifyBytesPerHour rations reading checkpoints back. Zero means 2 GiB; a negative value means
	// no limit (only for tests).
	VerifyBytesPerHour int64

	// Probes are the conditions this controller can watch. Nil means none, which is a valid
	// deployment: with nothing to report and no fallback, the job is never written.
	Probes []produce.Probe

	// AlwaysDecide makes every round decide, whatever the ledger says about when asking again is
	// worth it. It is for a one-shot caller (), which has no next round to
	// throttle: a process that runs once and exits must answer the question it was started for.
	AlwaysDecide bool

	// BlindAfter is how many consecutive unreadable readings make a condition blind. Zero means
	// three, the sampler's default; a negative value makes the first failure blind (tests only).
	BlindAfter int

	// Window is the user's `with-us`: the longest a checkpoint may stay stale while we are alive.
	// Zero means off — exceptions only — and the status then says what that hole costs. It is a
	// deadman, not a schedule: every write that seals restarts it (exec.Window).
	Window time.Duration

	// PlanTTL is how long a published plan stays authoritative. Zero means advise.DefaultPlanTTL. It
	// is a few rounds, not a few minutes: the plan's expiry means "a live governor exists", so a
	// dead one has to be noticed within a handful of rounds.
	PlanTTL time.Duration

	// Throughput is the storage's write speed in bytes per second. Zero means "measure once, when an
	// estimate is first needed"; a negative value means unknown (tests only). It is never a
	// measurement of a checkpoint, and it is never mixed into the measured write costs.
	Throughput float64

	// SecondsLeft is the job's own deadline, when it reports one (glue: seconds-left). It is a
	// function because the number changes every time the job writes it, and the loop reads it once
	// per round instead of holding a stale copy. When nil, the glue file read this round is used.
	SecondsLeft func() (time.Duration, bool)
	// JobEnding says the job has finished its own final write (glue: ending). It is the honest
	// moment to release: waiting on an attempt the job will never finish holds the user for a write
	// that already landed.
	JobEnding func() bool
	// StopRequested says the job has been told to stop (glue: stopping). It is what makes the
	// executor the other half of the user's stop: it releases the job once the final write is done.
	StopRequested func() bool
	// Teardown is how long the job needs after the final write. It is the margin the no-time gate
	// adds to one write before comparing it with the deadline.
	Teardown time.Duration
	// WriteEstimate is the duration to assume when neither a seal has been measured nor the job has
	// declared its own save time. Zero means unknown, and then the deadline arithmetic is skipped
	// rather than invented.
	WriteEstimate time.Duration

	// Holder identifies this controller for the directory lease. Empty means "no lease": every
	// process writes, which is what tests and single-shot commands want.
	Holder string
	// DryRun makes a round read-only: it observes, decides and reports what it *would* publish and
	// delete, but it writes nothing — not the plan, not the ledger, not the attempts, not the trash,
	// not the lease, not `may-exit`. It exists so a person can be told what would happen before
	// letting it happen (the plan/apply split every careful control plane has).
	DryRun bool
	// LeaseTTL is how long the lease stays valid without a renewal. Zero means fifteen seconds.
	LeaseTTL time.Duration

	// Volume names the storage this directory lives on. Empty means Dir, which keeps units on
	// different directories from blocking each other when nobody told us the volume.
	Volume string
	// Volumes is the cross-unit write limit: several units may share one volume, and the algorithm
	// cannot see the others, so the executor holds a slot from "a write was asked for" to "its
	// verdict arrived". Nil means no limit, which is a single-unit deployment.
	Volumes VolumeLimiter

	// Cluster is the cluster this unit's world belongs to. Empty means the single-cluster case; it
	// is carried so an event says which fleet the unit is in.
	Cluster string
	// Notify is told about every plan that changed, after it reached the disk. It is a report, not
	// an input: a failure is recorded in events.jsonl and the round carries on, because a receiver
	// that is down must never become a reason the job is not written.
	Notify func(notify.Event) error
}

// VolumeLimiter is the per-volume write gate. The executor owns the slots; the algorithm only says
// when a write is worth doing (decision 0016: a resource limit is not a second decision).
type VolumeLimiter interface {
	TryAcquire(volume string) bool
	Release(volume string)
}

// Step is what one round did, for the caller's log and the tests.
type Step struct {
	Phase     advise.Phase
	Action    policy.Action
	Reason    policy.Reason
	Published bool
	Deleted   []Deleted
	Failures  int
	Notes     []string
	// CostLo and CostHi bracket the write that sealed in this round, in seconds. Zero when no seal
	// was observed. The gap between them is the measurement error, and printing it is the point.
	CostLo, CostHi float64
	// Overhead is what this round cost, counted where the work happened.
	Overhead advise.Overhead
	// Cause and CauseWhy are this round's decision in a person's words: the condition that fired (or
	// the reason none did) and its one-line sentence. They exist so a run can print one readable line
	// per decision instead of a row of counters.
	Cause    string
	CauseWhy string
	// Decided is false when this round only observed: the policy was not called because nothing it
	// reads had changed.
	Decided bool
	// VerifyDeferred is true when a checkpoint was due for a read-back but the byte budget pushed
	// it to the next window.
	VerifyDeferred bool
	// Evidence is what the declared capabilities produced this round, and Signals is how many times
	// each has been read in total. Both are for the log and the tests; the policy sees Evidence.
	Evidence map[string]policy.Observation
	Signals  map[string]int
	// Conditions is each condition's answer this round, spelled as the producer gave it.
	Conditions map[string]advise.Condition
	// ReadOnly says another controller holds the lease: this round observed nothing and changed
	// nothing.
	ReadOnly bool
	// Attempts is the write history derived from the attempts, so a caller can print what happened
	// without recomputing it.
	Attempts exec.Counts
	// Gate is the reason the gate gave this round, empty when it was not asked.
	Gate string
	// Blind is how many conditions could not be read this round. It is a count, not a second
	// representation of the reasons: those are in Conditions, and the watchdog's death is the
	// watch.broken condition like every other answer.
	Blind int
}

// Loop owns one directory's state.
type Loop struct {
	cfg      Config
	ledger   advise.Ledger
	attempts exec.Ledger
	scanner  *observe.Scanner
	sampler  *produce.Sampler
	// window is the user's `with-us`, armed by the seal it observed and never by a request. It is the
	// only clock the executor owns (exec.Window).
	window   exec.Window
	readOnly bool
	// pendingGeneration is a generation counted outside the directory (the membership registry). It
	// is applied at the start of the next round, so a decision never sees half of the change.
	pendingGeneration int64
	// glue is what the job declared about itself this round: its own size, its own save duration, and
	// the deadline something announced. Numbers, not conditions — the algorithm never sees them.
	glue Glue
	// throughputOnce and throughputBytes are the storage probe: measured once, only if an estimate
	// ever needs it.
	throughputOnce  sync.Once
	throughputBytes float64
	// exitPhase is which phase of a stop the job reported last round (stopping without a final write,
	// final write in flight, final write landed). It is held so the stop is announced once per
	// transition instead of once per round.
	exitPhase int
	// lastGate is the reason the gate gave the last time it was asked. It is memory, not a file: a
	// restart forgets it, and the attempts still say what was refused.
	lastGate string
	// slots is which volume slot each running attempt holds, by attempt ID. It is memory for the
	// same reason a lock is: a restart releases every slot, because the process that held them is
	// gone.
	slots map[string]string
}

// Open loads the ledger. A controller restart increments the epoch, which is how the job learns that
// the plan it holds was produced before the restart.
func Open(cfg Config) (*Loop, error) {
	if cfg.Dir == "" {
		return nil, errors.New("loop: no checkpoint directory")
	}
	if cfg.Policy == nil {
		// The only shipped policy: it writes when a reported condition says the work is at risk,
		// and never otherwise. A deployment with no producers therefore writes nothing, which is
		// the state its user signed for (decisions 0013, 0015).
		cfg.Policy = policy.Risk{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	existed := fileExists(filepath.Join(cfg.Dir, advise.BaseDir, "internal.json"))
	if cfg.Volume == "" {
		cfg.Volume = cfg.Dir
	}
	l := &Loop{cfg: cfg, scanner: observe.NewScanner(), slots: map[string]string{}}
	if len(cfg.Probes) > 0 {
		l.sampler = produce.NewSampler(cfg.Probes...).BlindAfter(cfg.BlindAfter)
	}
	if cfg.Holder != "" && !cfg.DryRun {
		ok, err := lease.Acquire(cfg.Dir, cfg.Holder, cfg.leaseTTL(), cfg.Now())
		if err != nil {
			return nil, err
		}
		l.readOnly = !ok
	}
	led, err := advise.Load(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if existed {
		led.Epoch++
	} else if led.Epoch == 0 {
		led.Epoch = 1
	}

	// A crash between publishing and recording leaves a plan on disk that the ledger never saw.
	// The file is the newer truth: adopt its epoch and its advice, so the next round neither
	// re-advises nor forgets that the job was already asked.
	adopted := false
	var published advise.Published
	if p, ok := advise.ReadPublished(cfg.Dir); ok && p.Epoch >= led.Epoch {
		led.Epoch = p.Epoch + 1
		if led.Status.AdviceAt == 0 && p.Decision.SaveNow {
			led.Status.AdviceAt = p.PublishedAt.Unix()
			led.Status.AdviceAction = string(p.Decision.Action)
			led.Status.AdviceReason = string(p.Decision.Reason)
		}
		led.Reconciled++
		adopted, published = true, p
	}
	// A unit always has a first generation; zero means "no member has been seen", which is a state
	// the published file must never carry.
	if led.Generation == 0 {
		led.Generation = 1
	}
	l.ledger = led

	// The attempt history is loaded, not trusted: on a restart every running attempt is settled
	// against the directory before anything new is decided, and the counters come from the outcomes
	// rather than from a stored number that a crash could have left half-written.
	att, err := exec.OpenLedger(cfg.Dir)
	if err != nil {
		return nil, err
	}
	l.attempts = att
	// The other half of the crash window: the plan is on disk, the job may already be honouring it,
	// and the ledger saved in that round never happened — so no attempt explains the write that answers
	// it. The attempt is rebuilt from the file, and `Record`'s identity rules deduplicate it: the same
	// request is one row whether it arrived from the file or from a decision. The step is not in the
	// plan (the contract has no such field), so it is left at zero and settlement attributes the write
	// by time.
	if adopted && published.Decision.SaveNow {
		trigger, why := exec.TriggerOf(published.Decision)
		l.attempts.Record(exec.NewAttempt(cfg.UnitID, led.Generation, 0, trigger, why,
			published.PublishedAt, exec.Deadline(0, published.PublishedAt)))
	}
	// The window is the only clock this side owns. It is armed from the newest seal when there is one
	// (a restart must not forget it) and from now when there is not (cold start), with the measured
	// write time as its lead so the seal lands inside the span.
	//
	// A ledger is not a witness, so the arming happens in the first round, where the directory is
	// visible: the copy's own timestamp wins over the ledger's `SealAt`, which wins over "now" — the
	// order of how much a fact survives a crash (0016 R2).
	l.window = exec.NewWindow(cfg.Window)
	if led.HasSeal && led.Status.SealAt != 0 {
		l.window.Arm(time.Unix(led.Status.SealAt, 0), l.writeLead())
	}
	return l, nil
}

// writeLead is the measured write time as the window wants it: the p90 when there is one, zero when
// nothing has been measured and there is no honest number to use.
func (l *Loop) writeLead() time.Duration {
	if w, ok := p90(l.ledger.WriteCosts); ok && w > 0 {
		return time.Duration(w * float64(time.Second))
	}
	return 0
}

// NeedsAttention says whether this unit has something to do even when nothing outside has happened:
// a running write to settle, the window about to close, or the moment the policy asked to be consulted
// again. It reads memory only — no directory walk — so a supervisor can ask every unit every tick.
func (l *Loop) NeedsAttention(now time.Time) bool {
	if l.attempts.Count().Running > 0 {
		// A write is in flight: it has to be settled, and that is a scan, not an event.
		return true
	}
	if l.window.Due(now) {
		return true
	}
	return !now.Before(time.Unix(0, l.ledger.NextDecisionAt))
}

// decisionThrottle is how long a round waits before asking the policy again. It is the executor's
// number, not the algorithm's: the policy says what to do at a save point, never how often to look.
// One second is short enough to notice a write starting and long enough that a fast round cannot
// rewrite the plan a hundred times in a second.
const decisionThrottle = time.Second

// patience is how long a write request may go unanswered before the job is called stalled: three
// write-times, which is long enough for a job to finish what it was doing, and short enough that
// "the job ignored us" is visible inside one span. With no measurement the floor is thirty seconds.
func (l *Loop) patience() time.Duration {
	if lead := l.writeLead(); lead > 0 {
		if p := 3 * lead; p > 30*time.Second {
			return p
		}
	}
	return 30 * time.Second
}

// AdvanceGeneration records a generation the unit's membership registry counted. A generation only
// moves forward: an older number is a late message, not a rollback.
func (l *Loop) AdvanceGeneration(gen int64) {
	if gen > l.pendingGeneration {
		l.pendingGeneration = gen
	}
}

// adoptGeneration applies a generation change: the attempts of a dead generation are dropped rather
// than settled, because the job they described no longer exists and counting them would blame the
// new incarnation for the old one's missing write. The job's own timer starts again for the same
// reason: the fallback belongs to the incarnation, not to the directory.
func (l *Loop) adoptGeneration(gen int64, now time.Time) {
	if gen <= l.ledger.Generation {
		return
	}
	l.ledger.Generation = gen
	l.attempts.DropOlderGeneration(gen)
	l.releaseAllSlots()
	l.window.Arm(now, l.writeLead())
}

// reasonVolumeBusy is the gate's answer when another unit is already writing on this volume. It is
// not part of exec's vocabulary because the volume is shared state between units, and exec never
// sees another unit.
const reasonVolumeBusy = "volume-busy"

// runningIDs is the attempts that were unfinished before this round settled them.
func runningIDs(attempts []exec.Attempt) []string {
	var out []string
	for _, a := range attempts {
		if !a.Done() {
			out = append(out, a.ID)
		}
	}
	return out
}

// fault runs the crash-injection hook at a named point. A test uses it to stop a round where a killed
// process would have stopped; production leaves Fault nil and pays one nil check.
func (l *Loop) fault(point string) error {
	if l.cfg.Fault == nil {
		return nil
	}
	return l.cfg.Fault(point)
}

// releaseSlots gives back the volume slots of the attempts that just ended. A verdict is the end of
// the write from this process's point of view — the directory stopped changing, or the deadline
// passed — and holding the slot longer would block a volume nobody is writing to.
func (l *Loop) releaseSlots(wasRunning []string) {
	if l.cfg.Volumes == nil {
		return
	}
	for _, id := range wasRunning {
		if _, held := l.slots[id]; !held {
			continue
		}
		if a, ok := l.attempts.Find(id); ok && !a.Done() {
			continue // still in flight: it keeps its slot
		}
		delete(l.slots, id)
		l.cfg.Volumes.Release(l.cfg.Volume)
	}
}

// releaseAllSlots is what a generation change and a shutdown do: the process that held the slots is
// gone, and a stale slot would block a volume that nothing is writing to.
func (l *Loop) releaseAllSlots() {
	if l.cfg.Volumes == nil {
		return
	}
	for id := range l.slots {
		l.cfg.Volumes.Release(l.cfg.Volume)
		delete(l.slots, id)
	}
}

// Ledger exposes the state for tests and for a caller that wants to render it.
func (l *Loop) Ledger() *advise.Ledger { return &l.ledger }

// Attempts is the derived write history, for a caller that wants it without a round.
func (l *Loop) Attempts() exec.Counts { return l.attempts.Count() }

// Close gives up the lease. A controller that is shutting down on purpose should say so, instead of
// making the next one wait for the lease to expire.
func (l *Loop) Close() error {
	l.releaseAllSlots()
	if l.cfg.Holder == "" || l.cfg.DryRun {
		// A dry run never took the lease, so releasing it would touch a file it does not own.
		return nil
	}
	return lease.Release(l.cfg.Dir, l.cfg.Holder)
}

func (c Config) leaseTTL() time.Duration {
	if c.LeaseTTL <= 0 {
		return 15 * time.Second
	}
	return c.LeaseTTL
}

// retainedCopies is how many complete copies will still be on disk after retention runs: every
// complete copy, capped by the decision's KeepCopies when it asks for a cap. Zero means the cold
// start, where the new copy has nothing to coexist with.
func retainedCopies(copies []policy.Copy, keepCopies int) int {
	complete := 0
	for _, c := range copies {
		if c.State == policy.CopySealed || c.State == policy.CopyVerified {
			complete++
		}
	}
	if keepCopies > 0 && complete > keepCopies {
		return keepCopies
	}
	return complete
}

// attemptLogs is the attempt history in the shape the view prints. It is a copy of the tail, not a
// second history: the ledger is the truth, and the file that carries it to a person is written every
// round anyway.
func attemptLogs(attempts []exec.Attempt) []advise.AttemptLog {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]advise.AttemptLog, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, advise.AttemptLog{
			ID: a.ID, Trigger: string(a.Trigger), Outcome: string(a.Outcome), StartedAt: a.StartedAt,
		})
	}
	return out
}

// writeKind says why the write that produced this seal happened. The history already knows: at most
// one attempt is in flight, so the newest attempt is the one that asked for the copy that just
// landed. The step is deliberately not matched — a request names the step the job will report, and
// the job may seal a later one.
func (l *Loop) writeKind() string {
	var last exec.Attempt
	found := false
	for _, a := range l.attempts.Attempts {
		if !found || a.StartedAt.After(last.StartedAt) {
			last, found = a, true
		}
	}
	if !found {
		// A write nobody asked for (the job's own timer, or a job that writes before the first plan)
		// is still a write; it is neither the governor's exception nor the window's.
		return "exception"
	}
	return kindOf(last.Trigger, last.Reason)
}

// kindOf is the one place a write is classified by why it happened. It is asked three times: when a
// write seals (Kinds and the receipt), when a request is published (so the final protocol knows it
// asked), and when the gate refuses (so the run's last chance is stated).
func kindOf(trigger exec.Trigger, why string) string {
	switch {
	case trigger == exec.TriggerWindow:
		return "window"
	case trigger == exec.TriggerRepair:
		return "repair"
	case why == "job.ending":
		return "final"
	default:
		return "exception"
	}
}

// finalRefusal is what the run says when the last write could not be made. "Already on disk" is not
// a failure — the step is saved and there is nothing left to pay — and everything else is: the work
// since the newest copy is what the user loses, and it is said in the same sentence as the reason.
func finalRefusal(gate exec.Result) string {
	if gate.Reason == "already-saved" {
		return "already on disk at the current step"
	}
	if gate.Detail != "" && gate.Detail != gate.Reason {
		return "not made: " + gate.Reason + " (" + gate.Detail + ")"
	}
	return "not made: " + gate.Reason
}

// recordBooks fills the fields a view prints, from facts the loop already holds. A view must never
// recompute an estimate or read a directory, so the numbers are written down here once per round.
func (l *Loop) recordBooks(now time.Time) {
	s := &l.ledger.Status
	if s.StartedAt == 0 {
		s.StartedAt = now.Unix()
	}
	s.LastRoundAt = now.Unix()
	s.BaselineNone = l.cfg.Bounds.NoUs == policy.NoUsNone
	s.BaselineSeconds = 0
	if l.cfg.Bounds.NoUs == policy.NoUsEvery {
		s.BaselineSeconds = int64(l.cfg.Bounds.NoUsEvery.Round(time.Second).Seconds())
	}
	s.WindowSeconds = int64(l.cfg.Bounds.WithUs.Round(time.Second).Seconds())
	s.WindowSpanSeconds = s.WindowSeconds
	s.WindowInfeasible = l.window.Infeasible()
	s.WindowDueAt, s.WindowRefreshedAt = 0, 0
	if due := l.window.DueAt(); !due.IsZero() {
		s.WindowDueAt = due.Unix()
	}
	if r := l.window.RefreshedAt(); !r.IsZero() {
		s.WindowRefreshedAt = r.Unix()
	}
	s.Failure = l.failureState()
}

// newestSealTime is the birth time of the newest complete copy in the directory, which is when the
// job finished writing it. It is the window's base across a restart, because it is the one record of
// the seal that the job itself wrote and we cannot lose.
func newestSealTime(copies []policy.Copy) (time.Time, bool) {
	var newest *policy.Copy
	for i := range copies {
		c := &copies[i]
		if c.State != policy.CopySealed && c.State != policy.CopyVerified {
			continue
		}
		if c.CreatedAt.IsZero() {
			continue
		}
		if newest == nil || c.Step > newest.Step {
			newest = c
		}
	}
	if newest == nil {
		return time.Time{}, false
	}
	return newest.CreatedAt, true
}

// jobSaysDone is the job's half of the finish handshake: the user stopped it, or training ended.
// Both mean the work that exists now is the work that matters, so both get the same answer — one
// final write, and then `may-exit` from us (not from the job: the job cannot see what is in flight).
func (l *Loop) jobSaysDone() bool {
	if l.cfg.StopRequested != nil && l.cfg.StopRequested() {
		return true
	}
	return l.cfg.JobEnding != nil && l.cfg.JobEnding()
}

// failureState names the one thing that is wrong, in the order that a person needs to know it.
// Empty means nothing is wrong — and "nothing is wrong" is a fact, not the absence of a field.
func (l *Loop) failureState() string {
	if o := l.ledger.Status.FinalOutcome; strings.HasPrefix(o, "not made") {
		// A run that ended without its last state on disk has one fact worth reading, and it is not
		// "baseline unset" or "blind": it is the checkpoint that is missing.
		return "final-write-failed (" + o + ")"
	}
	if l.cfg.Bounds.NoUs == policy.NoUsUnset {
		return "baseline-unset (no-us was not answered: the saving and the fallback are unknown)"
	}
	if l.ledger.Failures >= 2 {
		return "broken (two writes failed in a row: a person has to look at the storage)"
	}
	if l.ledger.Status.Blind > 0 {
		return "blind (" + strconv.Itoa(l.ledger.Status.Blind) + " conditions unreadable: writing is the fail-closed answer)"
	}
	switch l.lastGate {
	case "no-time", "headroom", "volume-busy":
		return "refused (" + l.lastGate + "): the write we wanted could not be covered"
	}
	if l.readOnly {
		return "read-only (another controller holds the lease)"
	}
	return ""
}

// Reconcile takes one decision and delivers it. The name is controller-manager's, and the property
// it stands for is the same: level-triggered, idempotent, safe to call when nothing has changed.
func (l *Loop) Reconcile() (Step, error) {
	now := l.cfg.Now()
	var st Step

	// One writer per directory. A second controller that cannot take the lease still runs — it
	// observes, it reports — but it does not publish, delete or record anything, because two
	// writers on one plan is a plan nobody can trust. A dry run never takes the lease: taking it
	// would already be a write, and it would lock out the controller that is governing the directory.
	if l.cfg.Holder != "" && !l.cfg.DryRun {
		ok, err := lease.Acquire(l.cfg.Dir, l.cfg.Holder, l.cfg.leaseTTL(), now)
		if err != nil {
			return st, err
		}
		if !ok {
			// Another controller holds the lease: this one observes nothing and changes nothing, and it
			// gives back the volume slots it was holding — a slot held by a controller that no longer
			// writes is a volume nobody can write to.
			l.releaseAllSlots()
			st.ReadOnly = true
			st.Phase = l.ledger.Status.Phase
			return st, nil
		}
		l.readOnly = false
	}
	base := filepath.Join(l.cfg.Dir, advise.BaseDir)
	// What the job declared about itself is read once per round: its size, its own save duration and
	// any announced deadline. Numbers, not conditions.
	l.glue = readGlue(l.cfg.Dir)

	// A new boot id means the job restarted; decisions from the previous incarnation are stale. The
	// membership registry may also have counted a generation, and a boot id is by itself a new
	// generation on top of whatever the registry said.
	gen := l.ledger.Generation
	if l.pendingGeneration > gen {
		gen = l.pendingGeneration
	}
	if boot := readText(filepath.Join(base, "boot")); boot != "" && boot != l.ledger.BootID {
		if l.ledger.BootID != "" {
			gen++
		} else if gen == 0 {
			gen = 1
		}
		l.ledger.BootID = boot
	}
	l.adoptGeneration(gen, now)
	if l.ledger.UnitStartedAt == 0 {
		l.ledger.UnitStartedAt = now.Unix()
	}

	// Capabilities are read every round, at their own cadences; the assembler turns them into
	// Evidence. A capability that is absent is not in the map, which is exactly how the algorithm
	// is told it is missing.
	req := observe.Request{
		Dir:               l.cfg.Dir,
		Now:               now,
		UnitID:            l.cfg.UnitID,
		Generation:        l.ledger.Generation,
		Step:              readInt(filepath.Join(base, "step")),
		Progress:          readFloat(filepath.Join(base, "progress")),
		Seen:              l.ledger.Seen(),
		WriteCostSamples:  l.ledger.WriteCosts,
		WriteSecondsTotal: &l.ledger.WriteSecondsTotal,
		Failures:          l.ledger.Failures,
		Verified:          l.ledger.VerifiedSteps(),
		Mismatched:        l.ledger.MismatchedSteps(),
	}
	var ov advise.Overhead
	scanStart := time.Now()
	s, rep, err := l.scanner.Scan(req)
	if err != nil {
		return st, err
	}
	ov.ScanNS = time.Since(scanStart).Nanoseconds()
	ov.StatCalls += rep.Stats
	ov.ManifestReads += rep.Reads
	ov.Rounds = 1
	st.Notes = rep.Notes

	// Conditions are sampled after the facts they may depend on: the storage condition needs the
	// size of the newest checkpoint, and the probe should see the world this round, not the last.
	var readings map[string]produce.Reading
	if l.sampler != nil {
		readings = l.sampler.Sample(produce.Env{Dir: l.cfg.Dir, Now: now, CheckpointBytes: s.CheckpointBytes})
		st.Blind = len(l.sampler.Blind())
	}
	evidence := evidenceOf(readings)
	for name, o := range evidence {
		s.Evidence[name] = o
	}
	st.Evidence = evidence

	// The view prints what each condition said, in the producer's own words (absent / unknown / 0 /
	// 1). The ledger carries it so "why was nothing written" has an answer that is not a guess.
	if len(readings) > 0 {
		conds := make(map[string]advise.Condition, len(readings))
		for name, r := range readings {
			c := advise.Condition{}
			switch r.State {
			case produce.Absent:
				c.Value = "absent"
			case produce.Unknown:
				c.Value = "unknown"
				if r.Err != nil {
					c.Reason = r.Err.Error()
				}
			default:
				if r.Value {
					c.Value = "1"
				} else {
					c.Value = "0"
				}
			}
			// "Since" is when the value last changed, not when the last read happened: a condition
			// that has said 1 for two minutes must not look like it just flipped.
			if prev, had := l.ledger.Status.Conditions[name]; had && prev.Value == c.Value {
				c.Since = prev.Since
			} else {
				c.Since = now.Unix()
			}
			conds[name] = c
		}
		l.ledger.Status.Conditions = conds
		st.Conditions = conds
	}

	// Settle before deciding: an attempt is judged by the directory as this round sees it, so the
	// decision that follows knows whether the previous write happened.
	before := runningIDs(l.attempts.Attempts)
	l.attempts.Attempts, st.Attempts = Settle(l.attempts.Attempts, s, now)
	l.releaseSlots(before)

	// What this process had already seen before this round marked everything it looks at with "now".
	// It is the only way to tell "we watched it seal" from "it was already here when we started".
	priorSeen := l.ledger.Seen()
	for _, c := range s.Copies {
		name := dirName(c.Step)
		l.ledger.Observe(name, now)
		if c.State == policy.CopySealed {
			l.ledger.Observe(name+"/"+observe.ManifestName, now)
		}
	}

	// A seal is recorded once: the record is the proof of a finished write, and a duplicate would
	// make the history and the tax accounting lie.
	seen := l.ledger.Seen()

	// Before processing seals: every directory still without a manifest was, as of this moment,
	// not sealed yet. That sighting is one end of the bracket the write's cost is measured in.
	for _, c := range s.Copies {
		if c.State == policy.CopyUnverified {
			l.ledger.AbsentAt[dirName(c.Step)] = now.UnixNano()
		}
	}

	sealedNow := false
	for _, c := range s.Copies {
		if !complete(c) || (l.ledger.HasSeal && c.Step <= l.ledger.LastSealStep) {
			continue
		}
		sealedNow = true
		name := dirName(c.Step)
		started := seen[name]
		// When did the copy become complete? Three answers, in order of how much this process knows:
		// we saw the manifest in an earlier round; we saw the directory unsealed before and sealed
		// now; or it was already here when we started, and then the copy's own timestamp is the only
		// witness left. "When we first looked" is never an answer, because it is about us.
		_, dirKnown := priorSeen[name]
		manifestAt, watched := priorSeen[name+"/"+observe.ManifestName]
		ended := time.Time{}
		switch {
		case watched && !manifestAt.IsZero():
			ended = manifestAt
		case dirKnown:
			ended = seen[name+"/"+observe.ManifestName]
		case !c.CreatedAt.IsZero():
			ended = c.CreatedAt
		}
		if ended.IsZero() {
			ended = now
		}
		if ended.After(now) {
			// A checkpoint cannot have sealed in the future. If a copy's timestamp says it did, the
			// clock that wrote it disagrees with ours; the conservative reading is "it landed now".
			ended = now
		}

		// The write finished somewhere between the last round that saw no manifest and this one.
		// The upper bound is what the plan is made with: an underestimated write cost is a promise
		// broken by a bad day, and the lower bound is kept only so the error is visible.
		hi := ended.Sub(started).Seconds()
		lo := hi
		if absent, ok := l.ledger.AbsentAt[name]; ok && !started.IsZero() {
			lo = time.Unix(0, absent).Sub(started).Seconds()
			if lo < 0 {
				lo = 0
			}
			if lo > hi {
				lo = hi
			}
		}
		if started.IsZero() || hi < 0 {
			lo, hi = 0, 0
		}
		st.CostLo, st.CostHi = lo, hi

		seconds := hi
		delete(l.ledger.AbsentAt, name)
		s.Evidence["write.cost.lower"] = policy.Observation{Kind: policy.Number, Number: lo, Source: "loop"}
		l.ledger.Status.Remember(advise.WriteRecord{
			Step: c.Step, Bytes: c.Bytes, State: "sealed",
			StartedAt: started.Unix(), EndedAt: ended.Unix(), Seconds: seconds,
		})
		// The receipt: what this write bought, priced in staleness. The staleness at request time is
		// measured from the previous seal (zero before the first one, when there was nothing to be
		// stale); at the seal it is zero, because the copy is now the newest complete one.
		prevSeal := l.ledger.Status.SealAt
		kind := l.writeKind()
		stale := started.Sub(time.Unix(prevSeal, 0)).Seconds()
		if prevSeal == 0 {
			stale = started.Sub(time.Unix(l.ledger.Status.StartedAt, 0)).Seconds()
		}
		l.ledger.Status.LastReceipt = &advise.Receipt{
			Kind: kind, StaleAtRequest: stale, StaleAtSeal: 0, Cost: seconds,
		}
		if l.ledger.Status.Kinds == nil {
			l.ledger.Status.Kinds = map[string]int{}
		}
		l.ledger.Status.Kinds[kind]++
		if kind == "final" || strings.HasPrefix(l.ledger.Status.FinalOutcome, "asked") {
			// The end of a run is the one write that has no next chance: what became of it is said
			// as a sentence, because it is the last line a person reads. The write is still counted
			// under whatever asked for it — a stop is a condition, and the saving must not claim it.
			l.ledger.Status.FinalOutcome = fmt.Sprintf("sealed at step %d (%s)",
				c.Step, humanAge(time.Duration(seconds*float64(time.Second))))
		}
		l.ledger.LastSealStep = c.Step
		l.ledger.HasSeal = true
		l.ledger.WriteSecondsTotal += seconds
		if seconds > 0 {
			l.ledger.AddWriteCost(seconds)
		}
		l.ledger.Failures = 0
		l.ledger.LastFailedDir = ""
		l.ledger.Status.Failures = 0
		l.ledger.Status.SealStep = c.Step
		l.ledger.Status.SealAt = ended.Unix()
		// The window is restarted by the seal, never by the request: the promise is about the newest
		// complete checkpoint, so the period is measured seal to seal.
		l.window.Arm(ended, l.writeLead())
		if err := l.fault("window"); err != nil {
			// The kill lands between "we know when the checkpoint landed" and "the ledger remembers it".
			// The next process must not learn the window from the ledger: it is armed from the copy.
			return st, err
		}
		if l.ledger.Status.AdviceAt > 0 && l.ledger.Status.SeenAt == 0 && started.After(time.Unix(l.ledger.Status.AdviceAt, 0)) {
			l.ledger.Status.SeenAt = started.Unix()
		}
	}
	if sealedNow {
		if err := l.fault("seal"); err != nil {
			return st, err
		}
	}

	// A failure is counted once per directory: without that, a broken copy raises the count on every
	// round and the unit blocks itself within seconds.
	if len(rep.Failures) > 0 {
		f := rep.Failures[0]
		if f.Dir != l.ledger.LastFailedDir {
			l.ledger.Failures++
			l.ledger.LastFailedDir = f.Dir
			l.ledger.Status.Failures = l.ledger.Failures
			l.ledger.Status.Last = &advise.Failure{Code: f.Code, At: now.Unix(), Detail: f.Detail}
			l.ledger.Status.Remember(advise.WriteRecord{Step: stepOfDir(f.Dir), State: "broken", EndedAt: now.Unix()})
		}
	}

	if s.SaveInFlight {
		l.ledger.Status.WriteStep = stepOfDir(rep.InFlight)
		l.ledger.Status.WriteStartedAt = s.SaveStartedAt.Unix()
		l.ledger.Status.WriteProgress = s.SaveProgress
		if w, ok := p90(l.ledger.WriteCosts); ok {
			l.ledger.Status.WriteExpected = w
		}
	} else {
		l.ledger.Status.WriteStep = 0
		l.ledger.Status.WriteStartedAt = 0
		l.ledger.Status.WriteProgress = -1
	}

	// The snapshot was built before this round counted its failures; the decision must see the
	// count this round produced, or every rule about repeated failures lags by one round.
	s.ConsecutiveWriteFailures = l.ledger.Failures

	// The window's base is the checkpoint, not the ledger. A crash between observing a seal and saving
	// the ledger would otherwise restart the window from the *previous* seal (or from nothing at all),
	// which makes the bound weaker than the number the user was given. The newest sealed copy's own
	// timestamp is the truth and it outlives us — the same rule as attempts (0016 R2). With no copy
	// yet, the clock starts when we do: the job has not run anything worth saving.
	if l.cfg.Window > 0 && !l.window.Armed() {
		if at, ok := newestSealTime(s.Copies); ok {
			l.window.Arm(at, l.writeLead())
		} else {
			l.window.Arm(now, 0)
		}
	}

	// Measure often, decide rarely: the policy is free, but a new plan rewrites a file the job
	// watches. It is called only when its answer can have changed.
	windowDue := l.window.Due(now)
	decide := l.shouldDecide(now, s) || windowDue
	var d policy.Decision
	if decide {
		d = l.cfg.Policy.Decide(l.cfg.Bounds, s)
		ov.Decisions = 1
		l.ledger.DecidedSealStep = l.ledger.LastSealStep
		l.ledger.DecidedFailures = l.ledger.Failures
		l.ledger.WasInFlight = s.SaveInFlight
		// The window is the user's own number: when it closes, the write happens even if no condition
		// fired. It is a trigger, not a second policy.
		if windowDue && d.Action != policy.ActionSave {
			d = windowDecision(now)
		}
		// The end of a run is the other trigger that is not a condition: the job said it is finished,
		// and the answer to that is exactly one write (decision 0017; design-v1 §7).
		if l.jobSaysDone() && l.ledger.Status.FinalOutcome == "" && d.Action != policy.ActionSave {
			d = finalDecision(now)
		}
		l.ledger.LastDecision = d
		// The throttle is the executor's, not the algorithm's: "measure often, decide rarely" is a
		// rule about how much a round costs, so the algorithm says nothing about when to ask again.
		// A refusal that knows its own wait (a cooldown, a backoff) sets it in the gate branch below.
		l.ledger.NextDecisionAt = now.Add(decisionThrottle).UnixNano()
	} else {
		d = l.ledger.LastDecision
	}
	st.Decided = decide

	// The gate is a pure function of the decision, the history and the world this round scanned:
	// it is asked before anything is asked of the job, so a refused write never reaches it.
	gate := exec.Result{Verdict: exec.Start}
	held := false
	kept := false // the volume slot was handed to an attempt instead of returned
	// The event this round is looking at (0022). Read once, before the gate, so that the gate which may
	// refuse and the record which says the event was paid for cannot disagree about which event it is.
	seenCause := ""
	if l.cfg.CauseID != nil {
		seenCause = l.cfg.CauseID(now)
	}
	// The last chance is not a condition: once the job says it is done, whichever condition named this
	// write, this is the write nobody gets a second chance at. It is read once, before the gate, so the
	// whole round agrees about which write it is looking at.
	final := l.jobSaysDone() && l.ledger.Status.FinalOutcome == ""
	if decide && d.Action == policy.ActionSave {
		// Two numbers the gate needs and the algorithm must not see: how big one checkpoint is (the
		// estimate stands in for the measured size until the first seal), and how long a write takes.
		// The estimate channel is measured → declared by the job → probed storage; the config's
		// WriteEstimate is the last resort, and "unknown" skips the deadline arithmetic instead of
		// inventing a number.
		scale, estimated := sizeOf(s.CheckpointBytes, l.glue)
		writeSeconds, writeSource := l.estimate()
		if writeSeconds <= 0 && l.cfg.WriteEstimate > 0 {
			writeSeconds, writeSource = l.cfg.WriteEstimate.Seconds(), "assumed"
		}
		left, hasLeft := l.glue.SecondsLeft, l.glue.HasSeconds
		if l.cfg.SecondsLeft != nil {
			if d, ok := l.cfg.SecondsLeft(); ok {
				left, hasLeft = d.Seconds(), true
			}
		}
		trigger, why := exec.TriggerOf(d)
		env := exec.Env{
			Step:            s.Step,
			CheckpointBytes: scale,
			FreeBytes:       s.FreeBytes,
			Retained:        retainedCopies(s.Copies, d.KeepCopies),
			Copies:          d.CopiesToWrite,
			SealedStep:      l.ledger.LastSealStep,
			SecondsLeft:     left,
			HaveSeconds:     hasLeft,
			WriteSeconds:    writeSeconds,
			Teardown:        l.cfg.Teardown,
			Final:           final || kindOf(trigger, why) == "final",
		}
		// The fleet's verdict, when somebody is in a position to give one. A held unit publishes a
		// refusal rather than a plan that asks for a write nobody needs (0021).
		if l.cfg.Permit != nil {
			env.Permit, env.PermitUntil = l.cfg.Permit(now)
		}
		// One decision per event: the gate needs to know which event this is, and whether this unit
		// already answered it (0022). No id means nothing to compare, and an unreadable event writes.
		if seenCause != "" {
			env.CauseID = seenCause
			env.CauseHandled = l.ledger.CauseWrittenID == seenCause
		}
		if estimated {
			// A size we were told, not one we measured: the space rule applies, and the status says so.
			st.Notes = append(st.Notes, "headroom uses the declared size "+bytesHuman(scale)+" (estimated)")
		}
		gate = exec.Gate(d, l.attempts.Attempts, env, now)
		st.Gate = gate.Reason
		l.ledger.Status.GateDetail = gate.Detail
		l.lastGate = gate.Reason
		if gate.Warning != "" {
			// A rule the gate could not apply is said out loud: "the first write was not guarded" is
			// something a person has to be able to read, not something to hide behind a pass.
			st.Notes = append(st.Notes, gate.Warning)
		}
		if gate.Reason == "no-time" {
			// Refusing a write we cannot finish is only honest if the loss is stated: the window is the
			// platform's, and the work since the last checkpoint is what it costs. The gate owns the
			// arithmetic sentence; the loop adds the price.
			detail := gate.Detail
			if detail == "" {
				detail = exec.FormatNoTime(env)
			}
			st.Notes = append(st.Notes, fmt.Sprintf("gate: %s — %s of work at risk",
				detail, humanAge(now.Sub(time.Unix(l.ledger.Status.SealAt, 0)))))
		}
		l.ledger.Status.EstimateSeconds, l.ledger.Status.EstimateSource = writeSeconds, writeSource
		switch gate.Verdict {
		case exec.Hold:
			// The request is already being served. Publishing it again would rewrite a file the job is
			// reading, and recording it again would restart a deadline that is already counting down:
			// the attempt that covers this request is in the history, and this round adds nothing.
			held = true
		case exec.Refuse:
			// A condition that keeps firing must leave a trace even when the gate stops it, and the
			// job must be told "not now" instead of a save it would have to ignore.
			trigger, why := exec.TriggerOf(d)
			a := exec.NewAttempt(l.cfg.UnitID, l.ledger.Generation, s.Step, trigger, why, now, exec.Deadline(0, now))
			a.Outcome, a.EndedAt = exec.Refused, now
			l.attempts.Record(a)
			if final || kindOf(trigger, why) == "final" {
				// The final write is the one refusal nobody gets a second chance at: it is named here
				// so the end of the run says what was not saved, instead of ending in silence. "Already
				// on disk" is the exception: nothing was lost, so it is not called a failure.
				l.ledger.Status.FinalAskedAt = now.Unix()
				l.ledger.Status.FinalOutcome = finalRefusal(gate)
				if !strings.HasPrefix(l.ledger.Status.FinalOutcome, "already") {
					st.Notes = append(st.Notes, "final write NOT made: "+l.ledger.Status.FinalOutcome)
				}
			}
			d = heldDecision(d, gate, now)
			// A refusal that knows its own wait (a cooldown, a backoff) says when asking again is worth
			// it; every other refusal is retried at the ordinary cadence.
			if gate.Wait > 0 {
				l.ledger.NextDecisionAt = now.Add(gate.Wait + time.Second).UnixNano()
			}
			st.Attempts = l.attempts.Count()
		}
		// The volume is the one thing the algorithm cannot see: another unit may be writing to the
		// same disk, and a second write on top of it makes both slower and neither safer. A busy
		// volume is a refusal, not a hold: nothing of ours is in flight.
		if gate.Verdict == exec.Start && l.cfg.Volumes != nil {
			if !l.cfg.Volumes.TryAcquire(l.cfg.Volume) {
				trigger, why := exec.TriggerOf(d)
				a := exec.NewAttempt(l.cfg.UnitID, l.ledger.Generation, s.Step, trigger, why, now, exec.Deadline(0, now))
				a.Outcome, a.EndedAt = exec.Refused, now
				l.attempts.Record(a)
				l.lastGate = reasonVolumeBusy
				st.Gate = reasonVolumeBusy
				if final || kindOf(trigger, why) == "final" {
					l.ledger.Status.FinalAskedAt = now.Unix()
					l.ledger.Status.FinalOutcome = "not made: another unit is writing on this volume"
					st.Notes = append(st.Notes, "final write NOT made: "+l.ledger.Status.FinalOutcome)
				}
				d = heldDecision(d, exec.Result{Verdict: exec.Refuse, Reason: reasonVolumeBusy}, now)
				st.Attempts = l.attempts.Count()
			} else {
				// A slot that never reaches an attempt is given back before this round returns, on
				// every path including an error: a leaked slot blocks a volume forever.
				defer func() {
					if !kept {
						l.cfg.Volumes.Release(l.cfg.Volume)
					}
				}()
			}
		}
	}

	st.Action, st.Reason = d.Action, d.Reason

	// Retention may be expressed as a step (old policy) or as a count (risk-v1 keeps KeepCopies and
	// says nothing about a step): either is enough to retire, and "neither" means keep everything.
	if decide && (d.OldestStepToKeep > 0 || d.KeepCopies > 0) {
		delStart := time.Now()
		if l.cfg.DryRun {
			// A dry run says what would be removed without removing it: the selection is the same
			// function the real round uses, so the preview cannot drift from the act.
			for _, c := range retirableBelow(d.OldestStepToKeep, d.KeepCopies, s.Copies) {
				st.Deleted = append(st.Deleted, Deleted{Step: c.Step, Bytes: c.Bytes})
			}
		} else {
			// Under space pressure retention keeps fewer copies: writing into a full disk turns one bad
			// checkpoint into a busy volume, and the newest copy is the one anybody comes back for.
			keep := keepUnderPressure(l.cfg.Dir, d.KeepCopies, s.Copies)
			deleted, err := retireBelow(l.cfg.Dir, d.OldestStepToKeep, keep, s.Copies, now)
			if err != nil {
				return st, err
			}
			st.Deleted = deleted
			if len(deleted) > 0 {
				if err := l.fault("retire"); err != nil {
					return st, err
				}
			}
		}
		ov.DeleteNS += time.Since(delStart).Nanoseconds()
		ov.DeletedDirs += len(st.Deleted)
	}

	pubStart := time.Now()
	published := false
	// The request's identity is decided before the plan is published, because the plan must name it:
	// the job acts on a request at most once, keyed by this id, and the executor settles the write
	// that answers it by the same id. The attempt is still *recorded* after the publish — the crash
	// window the reconcile logic covers is "published but not recorded", and it must not turn into
	// "recorded but never published".
	var pending *exec.Attempt
	if decide && !held && d.SaveNow {
		trigger, why := exec.TriggerOf(d)
		cost, _ := p90(l.ledger.WriteCosts)
		a := exec.NewAttempt(l.cfg.UnitID, l.ledger.Generation, s.Step, trigger, why, now, exec.Deadline(cost, now))
		d.RequestID = a.ID
		pending = &a
	}
	if decide && !held && !l.cfg.DryRun {
		// Two different acts wear the same name here. A **renewal** re-stamps the same plan so the job
		// keeps seeing "a live governor exists"; a **change** overwrites a file the job may be reading.
		// Only the change may be rate-limited: a breaker that could also stop renewals would let the
		// plan expire while we are alive and healthy, which is the one lie this file must never tell
		// (a real-clock smoke with a 200ms cadence caught exactly that, at 103s of silence).
		changed := !advise.SameAsPublished(l.cfg.Dir, d)
		if !changed || l.publishAllowed(now) {
			ttl := l.cfg.PlanTTL
			published, err = advise.Publish(l.cfg.Dir, d, l.ledger.Epoch, now, ttl)
			if err != nil {
				return st, err
			}
			if published && changed {
				l.ledger.PublishTimes = append(l.ledger.PublishTimes, now.Unix())
				l.notifyChange(d, now)
			}
		} else {
			// The plan flapped past the limit. Keep the old file — a consistent stale plan beats a
			// file rewritten five times a second — and look again soon.
			l.ledger.NextDecisionAt = now.Add(time.Second).UnixNano()
		}
	}
	// The heartbeat. The decision is throttled on purpose ("measure often, decide rarely"); the plan's
	// expiry is not part of that trade — it means "a live controller exists". So the plan is re-stamped
	// whenever it is near expiry, whether or not this round took a decision, and without touching the
	// decision it carries. A plan that expires between two decisions tells a healthy job we are gone.
	renewed := false
	if !published && !l.cfg.DryRun {
		renewed, err = advise.Renew(l.cfg.Dir, now, l.cfg.PlanTTL)
		if err != nil {
			return st, err
		}
	}
	// Sweeping runs **every round**, bounded in bytes: a job that stops writing still has trash to give
	// back, and a time budget both overruns on a slow volume and under-delivers on a fast one. Whatever is
	// left is picked up next round.
	if !l.cfg.DryRun {
		swept, freed, err := sweepTrash(l.cfg.Dir, sweepBytesPerRound)
		if err != nil {
			return st, err
		}
		ov.SweptDirs += swept
		ov.SweptBytes += freed
		if swept > 0 {
			if err := l.fault("sweep"); err != nil {
				return st, err
			}
		}
	}
	ov.PublishNS += time.Since(pubStart).Nanoseconds()
	if published || renewed {
		// writeAtomic syncs the file and then the directory: two, and the count is exact because
		// the publisher does exactly that — a renewal is a write of the same file.
		ov.Fsyncs += 2
	}
	// "Published" in the round's report means "the job has a fresh plan to read", which is what a
	// renewal buys. The decision bookkeeping below is a different question and only follows a change.
	st.Published = published || renewed
	if err := l.fault("publish"); err != nil {
		return st, err
	}
	// A new **request** resets "was it seen": the job has a new question to answer. A renewal of the
	// same plan, or a plan that no longer asks for a write, leaves the answer alone.
	newRequest := d.SaveNow && (l.ledger.Status.AdviceAction != string(d.Action) || l.ledger.Status.AdviceReason != string(d.Reason))
	if published && newRequest {
		l.ledger.Status.AdviceAt = now.Unix()
		l.ledger.Status.SeenAt = 0
		l.ledger.Status.AdviceAction = string(d.Action)
		l.ledger.Status.AdviceReason = string(d.Reason)
	} else if published {
		l.ledger.Status.AdviceAction = string(d.Action)
		l.ledger.Status.AdviceReason = string(d.Reason)
	}

	// The attempt exists because we asked for a write, not because the job wrote: the request is the
	// unit of accounting, and its outcome is settled from the directory later (decision 0016 R2).
	if pending != nil && published {
		a := *pending
		l.attempts.Record(a)
		// The event is paid for. Recording it here — where the request becomes a real attempt, not
		// where it was merely considered — is what makes the next round of the same event refuse
		// instead of writing the same progress again (0022).
		if seenCause != "" {
			l.ledger.CauseWrittenID = seenCause
			l.ledger.CauseWrittenAt = now.Unix()
		}
		if final || kindOf(a.Trigger, a.Reason) == "final" {
			// The question is on its way; the answer is settled from the directory like every other
			// write, so a crash between the two cannot turn "asked" into "made".
			l.ledger.Status.FinalAskedAt = now.Unix()
			l.ledger.Status.FinalOutcome = "asked, no seal yet"
		}
		if l.cfg.Volumes != nil {
			// The slot now belongs to the attempt: it is released when the verdict arrives, not when
			// this round returns.
			l.slots[a.ID] = l.cfg.Volume
			kept = true
		}
		if err := l.fault("record"); err != nil {
			return st, err
		}
		// A write is on its way. The window is not armed here: a request is not a checkpoint, and
		// arming on the request would let "we tried" extend the span.
		st.Attempts = l.attempts.Count()
	}

	// The gap between publishing a save and seeing the directory is the job's read latency.
	if s.SaveInFlight && l.ledger.Status.AdviceAt > 0 && l.ledger.Status.SeenAt == 0 &&
		s.SaveStartedAt.After(time.Unix(l.ledger.Status.AdviceAt, 0)) {
		l.ledger.Status.SeenAt = s.SaveStartedAt.Unix()
	}

	// Verification runs after the plan is delivered: it must never delay a write, and its result is
	// for the next round anyway.
	verifyOv, deferred := l.maybeVerify(now, s)
	ov.Add(verifyOv)
	if deferred {
		st.VerifyDeferred = true
	}

	// The finish handshake, controller half: once the job says it is stopping or finished, we owe it an
	// answer, and `may-exit` is that answer (decision 0017 — we are the side that can see what is in
	// flight). It runs before the phase report, so the sentence a person reads is about what this round
	// actually did.
	l.releaseDoneJob(now, &st)
	l.observeExit(now, &st)

	st.Phase = phaseOf(l, s, d, now)
	l.ledger.Status.Phase = st.Phase
	l.ledger.Status.UnitID = l.cfg.UnitID
	l.ledger.Status.Generation = l.ledger.Generation
	// What the view prints is carried in the same file the view reads: the attempts, how many
	// conditions are blind, and why the gate last said no. They are assigned, not accumulated, so a
	// restart cannot inherit a number that described a process which is gone.
	l.ledger.Status.Attempts = attemptLogs(l.attempts.Attempts)
	l.ledger.Status.Blind = st.Blind
	l.ledger.Status.LastGate = l.lastGate
	// The window, the two numbers, the receipts and the one named failure: everything a view prints
	// is written here, once, from facts the loop already has.
	l.recordBooks(now)
	// Three different facts the view must not blur: what has never been looked at, what we wanted to
	// look at and could not afford, and what the budget is.
	l.ledger.Status.NewestUnverifiedAt = newestUnverified(s.Copies, l.ledger.Verified, l.ledger.Mismatch)
	l.ledger.Status.VerifyBudgetBytes = l.verifyBudget()
	l.ledger.Status.VerifyUsedBytes = l.ledger.VerifyBytesWindow
	l.ledger.Status.Overhead.Add(ov)
	if l.sampler != nil {
		// Cumulative read counts per condition: the view has to show that watching is cheap, and
		// which condition is expensive to watch.
		st.Signals = l.sampler.Reads()
		l.ledger.Status.Overhead.Signals = st.Signals
	}
	st.Overhead = ov
	st.Failures = l.ledger.Failures

	// A dry run reports the round and keeps no memory of it: recording would be a write, and a
	// preview that taught the controller something would change the next preview.
	if l.cfg.DryRun {
		return st, nil
	}
	if err := l.attempts.Save(l.cfg.Dir); err != nil {
		return st, err
	}
	if err := l.ledger.Save(l.cfg.Dir); err != nil {
		return st, err
	}
	// The words for this round are decided here, after the gate: what the line must explain is what
	// actually happened, and the gate is what turns a decision to save into a round that did not.
	st.Cause, st.CauseWhy = wordsFor(d, st.Action)
	return st, nil
}

// Round is the previous name of Reconcile. It stays because the callers live in the files this plan
// does not own (cmd/*), and a rename waiting for a coordinated commit is one alias, not two loops.
func (l *Loop) Round() (Step, error) { return l.Reconcile() }

// verifyBudget is how many bytes may be read back per hour. Zero means the default, a negative value
// means no limit.
func (l *Loop) verifyBudget() int64 {
	switch b := l.cfg.VerifyBytesPerHour; {
	case b < 0:
		return 0
	case b == 0:
		return 2 << 30 // 2 GiB: a 1 GB checkpoint can be checked roughly every half hour
	default:
		return b
	}
}

// rollVerifyWindow resets the hourly byte count when the hour has passed.
func (l *Loop) rollVerifyWindow(now time.Time) {
	if l.ledger.VerifyWindowStart == 0 || now.Unix()-l.ledger.VerifyWindowStart >= 3600 {
		l.ledger.VerifyWindowStart = now.Unix()
		l.ledger.VerifyBytesWindow = 0
	}
}

// publishAllowed is the circuit breaker: at most MaxPublishPerMinute writes in any sliding minute.
func (l *Loop) publishAllowed(now time.Time) bool {
	limit := l.cfg.MaxPublishPerMinute
	if limit < 0 {
		return true
	}
	if limit == 0 {
		limit = 12
	}
	cutoff := now.Add(-time.Minute).Unix()
	kept := l.ledger.PublishTimes[:0]
	for _, t := range l.ledger.PublishTimes {
		if t > cutoff {
			kept = append(kept, t)
		}
	}
	l.ledger.PublishTimes = kept
	return len(kept) < limit
}

// notifyChange posts one event for a plan that changed. A delivery failure is an event of its own
// and never an error of the round: the plan is already on disk, so the job will act on it whether
// or not a receiver heard about it.
func (l *Loop) notifyChange(d policy.Decision, now time.Time) {
	if l.cfg.Notify == nil {
		return
	}
	err := l.cfg.Notify(notify.Event{
		Unit:      l.cfg.UnitID,
		Cluster:   l.cfg.Cluster,
		At:        now,
		Action:    string(d.Action),
		Reason:    string(d.Reason),
		Why:       d.Why,
		RequestID: d.RequestID,
		Version:   d.Version,
	})
	if err != nil {
		_ = advise.Record(l.cfg.Dir, advise.Event{
			At:     now,
			Unit:   l.cfg.UnitID,
			Kind:   advise.EventError,
			Code:   "notify-failed",
			Values: map[string]any{"error": err.Error()},
		})
	}
}

// shouldDecide says whether the policy's answer can have changed since the last decision.
//
// The policy is a pure function of the snapshot, so "can it have changed" is a question about the
// snapshot: a new seal, a new failure, a write that started or finished, or the moment the last
// decision said it would be worth asking again.
func (l *Loop) shouldDecide(now time.Time, s policy.Snapshot) bool {
	if l.cfg.AlwaysDecide {
		return true
	}
	if l.jobSaysDone() && l.ledger.Status.FinalOutcome == "" {
		// The finish handshake has not been answered yet, and the job is waiting on the answer: this
		// round decides whatever the throttle says, or the job waits for a write we never asked for.
		return true
	}
	if l.ledger.NextDecisionAt == 0 {
		return true
	}
	if !now.Before(time.Unix(0, l.ledger.NextDecisionAt)) {
		return true
	}
	if l.ledger.HasSeal && l.ledger.LastSealStep != l.ledger.DecidedSealStep {
		return true
	}
	if l.ledger.Failures != l.ledger.DecidedFailures {
		return true
	}
	if s.SaveInFlight != l.ledger.WasInFlight {
		return true
	}
	return false
}

// newestUnverified is when the newest copy that has never been read back was first seen. A copy that
// was verified or found mismatched has been looked at, whatever the answer was.
func newestUnverified(copies []policy.Copy, verified map[int64]int64, mismatched map[int64]string) int64 {
	var newest *policy.Copy
	for i := range copies {
		c := &copies[i]
		if c.State != policy.CopySealed && c.State != policy.CopyVerified {
			continue
		}
		if _, ok := verified[c.Step]; ok {
			continue
		}
		if _, ok := mismatched[c.Step]; ok {
			continue
		}
		if newest == nil || c.Step > newest.Step {
			newest = c
		}
	}
	if newest == nil || newest.CreatedAt.IsZero() {
		return 0
	}
	return newest.CreatedAt.Unix()
}

// maybeVerify reads at most one checkpoint, at most once per interval, and only while nothing is
// being written. A read that competes with a write would make the write slower and the tax worse —
// the verification would damage the thing it is trying to prove.
func (l *Loop) maybeVerify(now time.Time, s policy.Snapshot) (advise.Overhead, bool) {
	every := l.cfg.VerifyEvery
	if every == 0 {
		every = 10 * time.Minute
	}
	if every < 0 || s.SaveInFlight {
		return advise.Overhead{}, false
	}
	if l.ledger.LastVerifyAt > 0 && now.Sub(time.Unix(l.ledger.LastVerifyAt, 0)) < every {
		return advise.Overhead{}, false
	}

	var pick *policy.Copy
	for i := range s.Copies {
		c := &s.Copies[i]
		if c.State != policy.CopySealed {
			continue
		}
		if _, done := l.ledger.Verified[c.Step]; done {
			continue
		}
		if _, bad := l.ledger.Mismatch[c.Step]; bad {
			continue
		}
		if pick == nil || c.Step > pick.Step {
			pick = c
		}
	}
	if pick == nil {
		return advise.Overhead{}, false
	}

	// Reading a checkpoint costs more than writing it, so it is rationed: a verification that would
	// exceed the hour's budget waits, and the waiting is counted, because "nothing is broken" slowly
	// becomes "we have not looked" and a person has to be able to tell the two apart.
	if budget := l.verifyBudget(); budget > 0 {
		l.rollVerifyWindow(now)
		est := pick.Bytes
		if est < 0 {
			est = 0
		}
		if l.ledger.VerifyBytesWindow+est > budget {
			l.ledger.Status.VerifyDeferred++
			return advise.Overhead{}, true
		}
	}

	vStart := time.Now()
	v, err := observe.Verify(l.cfg.Dir, pick.Step)
	ov := advise.Overhead{VerifyBytes: v.Bytes, VerifyNS: time.Since(vStart).Nanoseconds()}
	if err != nil {
		// A read error is not a verdict: the file may be busy or the disk may be slow. Try again
		// next time rather than calling a good checkpoint broken.
		return ov, false
	}
	l.ledger.LastVerifyAt = now.Unix()
	l.ledger.VerifyBytesWindow += v.Bytes
	if !v.Checked {
		return ov, false
	}
	if v.Detail == "" {
		l.ledger.Verified[pick.Step] = now.Unix()
		return ov, false
	}
	l.ledger.Mismatch[pick.Step] = v.Detail
	l.ledger.Failures++
	l.ledger.LastFailedDir = dirName(pick.Step)
	l.ledger.Status.Failures = l.ledger.Failures
	l.ledger.Status.Last = &advise.Failure{Code: "content_mismatch", At: now.Unix(), Detail: v.Detail}
	return ov, false
}

// phaseOf is the lifecycle the status view shows. Order matters: a blocked unit stays blocked even
// if the next decision would be a save, because a save is exactly what keeps failing.
func phaseOf(l *Loop, s policy.Snapshot, d policy.Decision, now time.Time) advise.Phase {
	switch {
	case l.ledger.Failures >= 2:
		return advise.PhaseBlocked
	case s.SaveInFlight:
		return advise.PhaseWriting
	case newestBroken(s.Copies):
		return advise.PhaseBroken
	case d.SaveNow:
		return advise.PhaseDue
	case stalled(l.ledger.Status, now, l.patience()):
		return advise.PhaseStalled
	case l.ledger.LastSealStep > 0:
		return advise.PhaseSealed
	default:
		return advise.PhaseIdle
	}
}

func newestBroken(copies []policy.Copy) bool {
	var newest *policy.Copy
	for i := range copies {
		if newest == nil || copies[i].Step > newest.Step {
			newest = &copies[i]
		}
	}
	return newest != nil && newest.State == policy.CopyBroken
}

// stalled says the plan asked for a write and the directory never showed one.
func stalled(s advise.Status, now time.Time, interval time.Duration) bool {
	if s.AdviceAt == 0 || s.SeenAt != 0 || interval <= 0 {
		return false
	}
	if s.AdviceAction != string(policy.ActionSave) && s.AdviceAction != string(policy.ActionRepair) {
		return false
	}
	return now.Sub(time.Unix(s.AdviceAt, 0)) > 3*interval
}

// evidenceOf turns capability values into the contract's observations.
//
// An unknown capability is left out on purpose: Evidence is what was measured, and a missing key is
// how the algorithm is told to say so in Missing. Writing a zero here would be a measurement that
// never happened.
func evidenceOf(readings map[string]produce.Reading) map[string]policy.Observation {
	out := make(map[string]policy.Observation, len(readings))
	for name, r := range readings {
		switch r.State {
		case produce.Absent:
			// No key: how the algorithm is told this environment has no such condition. Writing a
			// zero here would be a measurement that never happened.
			continue
		case produce.Unknown:
			out[name] = policy.Observation{Kind: policy.Unknown, At: r.At, Source: "probe"}
		default:
			out[name] = policy.Observation{Kind: policy.Boolean, Bool: r.Value, At: r.At, Source: "probe"}
		}
	}
	return out
}

// complete mirrors the observer's rule: a verified copy is a sealed copy that was read back.
func complete(c policy.Copy) bool {
	return c.State == policy.CopySealed || c.State == policy.CopyVerified
}

func dirName(step int64) string { return "step-" + strconv.FormatInt(step, 10) }

func stepOfDir(name string) int64 {
	rest, ok := strings.CutPrefix(name, "step-")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// p90 is the same statistic the policy plans for: the cost a bad day produces.
func p90(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	ordered := append([]float64(nil), values...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j] < ordered[j-1]; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	idx := (len(ordered)*9+9)/10 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ordered) {
		idx = len(ordered) - 1
	}
	return ordered[idx], true
}

func readText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func readInt(path string) *int64 {
	text := readText(path)
	if text == "" {
		return nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func readFloat(path string) *float64 {
	text := readText(path)
	if text == "" {
		return nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil
	}
	return &v
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// wordsFor turns a decision into the two things a person needs from a line of log: which condition
// spoke, and what it says. Counters answer "did something happen"; this answers "why", which is the
// only part a user can check against what they know about their job.
func wordsFor(d policy.Decision, action policy.Action) (string, string) {
	// A skip is explained by why no write started, not by the condition that is still true. The
	// condition is what made us look; "skip because preemption" is a contradiction on one line.
	if action != policy.ActionSave && action != policy.ActionRepair {
		// The gate names its own reason in the decision's sentence; the first word is the code the
		// rest of the system uses (cooldown, no-time, blocked), and anything after it is the
		// explanation. Using that word keeps one vocabulary across the line, status and explain.
		if held := strings.TrimPrefix(d.Why, "not starting a write: "); held != d.Why {
			if i := strings.Index(held, " "); i > 0 {
				return held[:i], strings.TrimSpace(held[i+1:])
			}
			return held, ""
		}
		if d.Why != "" {
			return string(d.Reason), d.Why
		}
		switch d.Reason {
		case policy.ReasonCalm:
			return "calm", "nothing says the work is at risk"
		}
		return string(d.Reason), ""
	}
	// A condition that could not be read is not the same fact as one that is true, and the policy
	// already writes the difference into the decision: "cannot read risk.preempt: …". The line
	// keeps that wording instead of repeating the sentence of a condition we never read.
	blind := strings.TrimPrefix(d.Why, "cannot read ")
	for _, r := range d.Reasons {
		if c, ok := policy.ConditionNamed(r.Code); ok {
			if strings.HasPrefix(blind, r.Code+":") {
				return r.Code, "cannot read: " + strings.TrimSpace(strings.TrimPrefix(blind, r.Code+":"))
			}
			return r.Code, c.Why
		}
		switch r.Code {
		case "window":
			return "window", "the window you set is about to close"
		case "first":
			return "first", "the first checkpoint of this run"
		}
		return r.Code, ""
	}
	switch d.Reason {
	case policy.ReasonCalm:
		return "calm", "nothing says the work is at risk"
	case policy.ReasonRisk:
		return "risk", "a condition says the work is at risk"
	case policy.ReasonInfeasible:
		return "infeasible-bounds", "the bounds cannot be satisfied"
	}
	return string(d.Reason), ""
}

// Receipt is what the run did, in numbers a person can check afterwards: how long it ran, how many
// writes, what they cost, why each happened, and what the user's own habit would have written.
//
// Every claim this product makes is about a run that has already finished. The live lines are for
// watching; this is the artifact someone reads once and keeps.
type Receipt struct {
	Duration time.Duration
	// Asked is how many writes we published a plan for; Sealed is how many of those we saw land.
	// They differ when a write is in flight at exit, and the difference is the honest answer to
	// "what did this thing do".
	Asked   int
	Sealed  int
	Seconds float64
	Share   float64 // seconds spent writing, as a share of the run
	Kinds   map[string]int
	// NoUs and Baseline: what the job would have done without us, in the same time. Zero NoUs means
	// the answer was none, and then there is no baseline to compare with.
	NoUs     time.Duration
	Baseline int
	Window   time.Duration
	// OnDisk is how many sealed checkpoints exist, whoever wrote them. A receipt that says "no
	// writes" while twenty checkpoints sit on disk is the kind of lie this product exists to
	// replace: the job may have its own timer, and that is worth saying out loud.
	OnDisk int
}

// countSealed counts the directories that carry a manifest. It walks the same shape verify.py does,
// one level deep, because a receipt is not the place to load a checkpoint.
func countSealed(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		// Both layouts, because the contract's writer (0025) names its directories by generation. Counting
		// only `step-` made every checkpoint written under the contract invisible here, and the receipt
		// then said "no writes" immediately after a write (F11).
		if !e.IsDir() || !(strings.HasPrefix(e.Name(), "step-") || strings.HasPrefix(e.Name(), "gen-")) {
			continue
		}
		if fileExists(filepath.Join(dir, e.Name(), "manifest.json")) {
			n++
		}
	}
	return n
}

// Receipt reads the counters the loop already keeps; it recomputes nothing, because a receipt that
// disagrees with the log is worse than no receipt.
func (l *Loop) Receipt() Receipt {
	started := l.ledger.UnitStartedAt
	if l.ledger.Status.StartedAt != 0 {
		started = l.ledger.Status.StartedAt
	}
	d := time.Since(time.Unix(started, 0))
	if started == 0 || d < 0 {
		d = 0
	}
	r := Receipt{
		Duration: d,
		Sealed:   len(l.ledger.WriteCosts),
		Seconds:  l.ledger.WriteSecondsTotal,
		Kinds:    map[string]int{},
		NoUs:     l.cfg.Bounds.NoUsEvery,
		Window:   l.cfg.Window,
		OnDisk:   countSealed(l.cfg.Dir),
	}
	for k, v := range l.ledger.Status.Kinds {
		r.Kinds[k] = v
		r.Asked += v
	}
	if d > 0 {
		r.Share = r.Seconds / d.Seconds()
	}
	if r.NoUs > 0 {
		r.Baseline = int(d / r.NoUs)
	}
	return r
}

// Lines renders the receipt: one fact per line, each with its number, and a last line that states the
// bound the user's own window bought — the thing they paid for.
func (r Receipt) Lines() []string {
	var out []string
	switch {
	case r.Asked == 0 && r.OnDisk == 0:
		out = append(out, fmt.Sprintf("run %s · no writes", coarseAge(r.Duration)))
		out = append(out, "  nothing said the work was at risk")
	case r.Asked == 0:
		// The job was writing on its own the whole time. Saying "no writes" here would be the lie
		// this product exists to replace; what is true is that none of them were ours.
		out = append(out, fmt.Sprintf("run %s · we asked for nothing · %s on disk",
			coarseAge(r.Duration), plural(r.OnDisk, "checkpoint")))
	default:
		if r.Seconds > 0 {
			out = append(out, fmt.Sprintf("run %s · %s · %s (%.2f%%)",
				coarseAge(r.Duration), plural(r.Asked, "write"),
				coarseAge(time.Duration(r.Seconds*float64(time.Second))), r.Share*100))
		} else {
			// A seal we saw but did not time. Printing "0ms (0.00%)" would be a measured claim
			// about something never measured.
			out = append(out, fmt.Sprintf("run %s · %s", coarseAge(r.Duration), plural(r.Asked, "write")))
		}
		if r.OnDisk > r.Asked {
			out = append(out, fmt.Sprintf("  %d more appeared from the job's own timer", r.OnDisk-r.Asked))
		}
	}

	keys := make([]string, 0, len(r.Kinds))
	for k := range r.Kinds {
		keys = append(keys, k)
	}
	// Kinds counts requests; WriteCosts counts seals. They are different facts, so they are printed
	// together only when both exist: a request that never sealed belongs in the log, not in a receipt.
	if r.Asked > 0 {
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, fmt.Sprintf("  %d on %s", r.Kinds[k], kindWords(k)))
		}
	}
	if r.Baseline > 0 {
		out = append(out, fmt.Sprintf("  your own habit (no-us %s) would have made %d in this time",
			coarseAge(r.NoUs), r.Baseline))
	}
	return append(out, r.BoundLine())
}

// BoundLine is the same sentence whether or not anything was written: the hole does not depend on the
// run having been busy.
func (r Receipt) BoundLine() string {
	if r.Window <= 0 {
		return "  a silent death costs everything: no window was set"
	}
	return fmt.Sprintf("  a silent death costs at most %s", coarseAge(r.Window))
}

// kindWords is how a receipt names a cause. The internal names are the policy's vocabulary; a receipt
// is read by someone who has never seen it.
func kindWords(k string) string {
	switch k {
	case "window":
		return "the window you set"
	case "exception":
		return "a condition"
	case "final":
		return "the end of the run"
	case "first":
		return "the first write"
	}
	return k
}

// coarseAge is how a receipt says length: one unit, no decimal. "run under a minute" is a sentence
// about a wait, not about a run, and a receipt is read once — it should not need a decoder.
func coarseAge(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

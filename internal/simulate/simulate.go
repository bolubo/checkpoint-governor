// Package simulate replays a recorded trace through a policy and reports the numbers the promise
// is made of: how long the newest checkpoint was too old, and how much of the time was spent
// writing.
//
// A trace describes the world, not a schedule: how fast steps ran, how much space was left, when a
// job was killed, and how long a write took. The schedule under test is the one being simulated, so
// the trace's own saves are read for one thing only — the cost of a write.
//
// This is the only replay engine in the repository. The algorithm area generates traces and scores
// controllers with it (score.go, gen.go); the delivery layer calls it from ckptctl. A second
// engine would drift, and then two tables would disagree about the same run.
package simulate

import (
	"errors"
	"sort"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Options are the numbers a user would set, plus the schedule under test.
//
// Exactly one of Policy and FixedPeriod is used: a policy decides from the snapshot, a fixed period
// decides from the clock, and the comparison between the two is the point of the table.
type Options struct {
	Bounds policy.Bounds
	// Eval is the yardstick the table is measured against; the controller never sees it.
	Eval        EvalBounds
	Policy      comparator
	FixedPeriod time.Duration
	// CostAt is the true duration of a write started at a given offset from the start of the trace.
	// A generated trace knows it; a recorded one does not, and then the replay falls back to the
	// samples the trace carries, interpolated. The distinction matters: interpolating two points
	// half an hour apart invents a ramp that the world never had.
	CostAt func(time.Duration) time.Duration
	// DeltaShare is how long a delta takes per second of training it covers (the alpha of
	// the retired interval arithmetic (0014)). Nil means this trace's storage cannot write deltas.
	DeltaShare func(time.Duration) float64
	// Model, when set, prices each write from its bytes, its streams and the per-write overhead,
	// instead of using the recorded costs. It is how the axes inside a write (content, shards,
	// overhead) are studied before a producer can implement them.
	Model *WriteModel
	// WarnBefore stands in for a producer of risk conditions: while a recorded kill is this close,
	// the replay reports risk.preempt; when the trace is this close to its end, job.ending; and while
	// free space is below two checkpoints, store.low-space. Zero means no conditions are reported at
	// all, which is the honest behaviour of a deployment without producers: nothing fires, nothing
	// is written. It is a modelled warning window, not a measurement; the real one is what a
	// deployment has to measure (decision 0014 §8).
	WarnBefore time.Duration
}

// Result is one row of the comparison table.
type Result struct {
	Policy       string
	Elapsed      time.Duration
	Violation    float64 // share of elapsed time with the age above the bound
	Tax          float64 // write seconds over elapsed seconds
	Saves        int
	SavesPerHour float64
	WriteCost    time.Duration
	Events       int
	// Infeasible and Unknown count decisions that refused to plan or could not measure. A refusal
	// that is not counted looks like a policy that does not write.
	Infeasible int
	Unknown    int
	Truncated  int
	// KillAges is the age of the newest checkpoint at each recorded interruption, -1 when there was
	// none. MeanKillAge averages the non-negative entries.
	MeanKillAge time.Duration
	// WindowViolation and WindowTax split the run into windows of one lost-work bound each, so the
	// scorer can ask what the best fixed period would have been for that window alone.
	WindowViolation []float64
	WindowTax       []float64
	// WindowBest is filled by Score, not by Run.
	WindowBest []time.Duration
	// MeanAge is the time-averaged age of the newest complete checkpoint, counted after the first
	// seal. It is the lost work an interruption at a random moment would have cost.
	MeanAge time.Duration
}

// world is the state the replay carries between events.
type world struct {
	now      time.Time
	start    time.Time
	end      time.Time     // the last recorded moment; nothing is accrued beyond it
	bound    time.Duration // the lost-work bound the violation ratio is measured against
	step     int64
	free     int64
	volume   int64
	ckpt     int64
	copies   []policy.Copy
	lastSeal time.Time

	inFlight  bool
	saveStart time.Time
	saveAt    time.Time
	willSeal  bool

	// costAt is how long a write started at a given moment takes. It is a curve, not a constant:
	// a trace that recorded a write during a storage storm carries the spike, and the replay must
	// be able to model a policy that reacts to it.
	costAt      func(time.Duration) time.Duration
	deltaShare  func(time.Duration) float64
	model       *WriteModel
	writeCosts  []float64
	stepTimes   []float64
	prevStepAt  time.Time
	prevStep    int64
	traceSaveAt time.Time

	// saveKind is what the running write produces: a full snapshot or a delta. It is what the
	// recorded cost and the retention rule are applied to.
	saveKind policy.WriteKind

	// ageSeconds is the time integral of the age, used for the mean age.
	ageSeconds float64
	// fullCosts are the completed *full* writes only. WriteCostSamples mixes both kinds, and a
	// policy that writes deltas needs the cost of a full snapshot on its own.
	fullCosts []float64

	window      time.Duration
	winViolated []time.Duration
	winJudged   []time.Duration
	winWrite    []time.Duration

	// accounting
	violation  time.Duration
	writeTime  time.Duration
	saves      int
	infeasible int
	unknown    int
	truncated  int
	killAges   []time.Duration
	// warnBefore and kills are the modelled producer (see Options.WarnBefore): zero means the
	// deployment reports no conditions at all.
	warnBefore time.Duration
	kills      []time.Time
}

// Run replays the trace under the policy and returns the metrics.
func Run(tr Trace, opts Options) (Result, error) {
	if len(tr.Events) == 0 {
		return Result{}, errors.New("the trace has no events")
	}

	cost, curve, ok := writeCostOf(tr)
	if !ok {
		// Without one completed write there is no cost to plan with, and inventing one would make
		// the table a statement about the invention.
		return Result{}, errors.New("the trace has no completed write: the cost of a write is unknown")
	}
	if opts.CostAt != nil {
		curve = opts.CostAt
	}

	start := tr.Events[0].T
	end := tr.Events[len(tr.Events)-1].T
	nWin := windowsOf(end.Sub(start), opts.Eval.MaxLostWork)
	window := end.Sub(start) / time.Duration(nWin)

	w := world{
		now:         start,
		start:       start,
		end:         end,
		free:        -1,
		volume:      -1,
		bound:       opts.Eval.MaxLostWork,
		costAt:      curve,
		deltaShare:  opts.DeltaShare,
		model:       opts.Model,
		warnBefore:  opts.WarnBefore,
		window:      window,
		winViolated: make([]time.Duration, nWin),
		winJudged:   make([]time.Duration, nWin),
		winWrite:    make([]time.Duration, nWin),
	}
	if opts.WarnBefore > 0 {
		// The stand-in for a producer: the moments a kill was recorded. A real deployment gets them
		// from the scheduler, before the kill, not after it.
		for _, e := range tr.Events {
			if e.Kind == KindKill {
				w.kills = append(w.kills, e.T)
			}
		}
	}

	planner := policyPlanner(opts)
	if opts.Policy == nil {
		planner = fixedPlanner(opts.FixedPeriod)
	}

	idx := 0
	var nextDecision time.Time
	var pending plan

	for iter := 0; iter < 1_000_000; iter++ {
		if idx >= len(tr.Events) && !w.inFlight {
			break
		}
		next := nextMoment(w, tr, idx, nextDecision)
		if next.IsZero() {
			break
		}
		if next.Before(w.now) {
			next = w.now
		}
		accrue(&w, w.now, next)
		w.now = next

		for idx < len(tr.Events) && !tr.Events[idx].T.After(w.now) {
			apply(&w, tr.Events[idx])
			idx++
		}
		if w.inFlight && !w.now.Before(w.saveAt) {
			seal(&w)
		}
		if w.inFlight {
			nextDecision = time.Time{}
			continue
		}
		// The world ends at the last recorded event. A save advised at that moment can only be
		// truncated, and asking again at the same instant would loop forever: the write cannot
		// complete, the next attempt cannot start either, and nothing about the answer can change.
		if idx >= len(tr.Events) && !w.now.Before(w.end) {
			break
		}

		d := planner(w.now, snapshotOf(w))
		pending = d
		switch d.Action {
		case policy.ActionInfeasible:
			w.infeasible++
		case policy.ActionUnknown:
			w.unknown++
		}
		if d.SaveNow || d.Action == policy.ActionRepair {
			startSave(&w, pending)
			nextDecision = time.Time{}
			continue
		}
		nextDecision = d.NextCheck
		if nextDecision.IsZero() || !nextDecision.After(w.now) {
			// The policy gave no moment to ask again. Asking on a fixed one-second timer would make
			// the number of decisions a property of this simulator rather than of the trace: at a
			// ten-times-larger world the same trace produced a different count. Nothing can change
			// without an event or a seal, so the next event is the next decision point.
			nextDecision = time.Time{}
		}
	}

	res := Result{
		Policy:    policyName(opts),
		Elapsed:   end.Sub(start),
		Events:    len(tr.Events),
		WriteCost: cost,
	}
	judged := end.Sub(start)
	res.Violation = ratio(w.violation, judged)
	if w.lastSeal.IsZero() {
		// A run that never produced a checkpoint has no age to measure and also never kept the
		// promise. A perfect zero here would make a refusing policy look best.
		res.Violation = 1
	}
	res.Tax = ratio(w.writeTime, judged)
	res.Saves = w.saves
	res.Infeasible = w.infeasible
	res.Unknown = w.unknown
	res.Truncated = w.truncated
	hours := judged.Hours()
	if hours > 0 {
		res.SavesPerHour = float64(w.saves) / hours
	}
	res.WindowViolation = make([]float64, nWin)
	res.WindowTax = make([]float64, nWin)
	for i := 0; i < nWin; i++ {
		res.WindowViolation[i] = ratio(w.winViolated[i], w.winJudged[i])
		res.WindowTax[i] = ratio(w.winWrite[i], w.window)
	}
	if judged > 0 {
		res.MeanAge = time.Duration(w.ageSeconds / judged.Seconds() * float64(time.Second))
	}
	var sum time.Duration
	var n int
	for _, age := range w.killAges {
		if age >= 0 {
			sum += age
			n++
		}
	}
	if n > 0 {
		res.MeanKillAge = sum / time.Duration(n)
	}
	return res, nil
}

// windowsOf divides a run into windows of one lost-work bound each, between two and eight. A
// quarter of the run would be too short to contain a full cycle when the bound is long, and
// "the best number for this hour" would then measure the window, not the policy.
func windowsOf(d, bound time.Duration) int {
	if bound <= 0 {
		return 4
	}
	n := int(d / bound)
	if n < 2 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}

func nextMoment(w world, tr Trace, idx int, decision time.Time) time.Time {
	var next time.Time
	if idx < len(tr.Events) {
		next = tr.Events[idx].T
	}
	if !decision.IsZero() && (next.IsZero() || decision.Before(next)) {
		next = decision
	}
	if w.inFlight && (next.IsZero() || w.saveAt.Before(next)) {
		next = w.saveAt
	}
	return next
}

// apply folds one environment event into the world. The trace's own saves are not replayed: they
// are read for one thing only, how long a write takes.
func apply(w *world, e Event) {
	switch e.Kind {
	case KindStep:
		if !w.prevStepAt.IsZero() && e.T.After(w.prevStepAt) && e.Step > w.prevStep {
			perStep := e.T.Sub(w.prevStepAt).Seconds() / float64(e.Step-w.prevStep)
			w.stepTimes = appendWindow(w.stepTimes, perStep)
		}
		w.step = e.Step
		w.prevStep = e.Step
		w.prevStepAt = e.T
	case KindSaveStart:
		w.traceSaveAt = e.T
	case KindSaveEnd:
		if !w.traceSaveAt.IsZero() && e.T.After(w.traceSaveAt) {
			w.writeCosts = appendWindow(w.writeCosts, e.T.Sub(w.traceSaveAt).Seconds())
		}
		if e.Bytes > 0 {
			w.ckpt = e.Bytes
		}
	case KindFree:
		w.free = e.Bytes
		if w.volume <= 0 && w.free > 0 {
			// The volume is not in the trace. Assume the space seen plus room for three copies, so
			// capacity is exercised without dominating.
			w.volume = w.free + 3*w.ckpt
		}
	case KindKill:
		// The promise is about the age of the newest checkpoint, not about surviving a kill.
		// Recording the age is what makes the kill useful: it is the work a resume would lose.
		if w.lastSeal.IsZero() {
			w.killAges = append(w.killAges, -1)
		} else {
			w.killAges = append(w.killAges, e.T.Sub(w.lastSeal))
		}
	}
}

// accrue adds one interval to the two clocks the promise is measured on.
//
// Nothing is accrued past the last recorded moment, so a save that would finish after the trace
// ends does not invent write time the world never saw.
func accrue(w *world, from, to time.Time) {
	if w.end.Before(to) {
		to = w.end
	}
	if !to.After(from) {
		return
	}
	if w.inFlight {
		// A write holds the job: its time is spent, and it is the tax the table reports.
		w.writeTime += to.Sub(from)
		w.winWrite[w.slot(from)] += to.Sub(from)
	}
	if w.bound <= 0 || w.lastSeal.IsZero() {
		w.winJudged[w.slot(from)] += to.Sub(from)
		return
	}
	w.winJudged[w.slot(from)] += to.Sub(from)
	// The age grows linearly between two seals, so its integral is a trapezoid.
	a0 := from.Sub(w.lastSeal).Seconds()
	a1 := to.Sub(w.lastSeal).Seconds()
	w.ageSeconds += (a0 + a1) / 2 * to.Sub(from).Seconds()
	limit := w.lastSeal.Add(w.bound)
	if limit.Before(to) {
		begin := from
		if limit.After(begin) {
			begin = limit
		}
		w.violation += to.Sub(begin)
		w.winViolated[w.slot(begin)] += to.Sub(begin)
	}
}

// slot is which window a moment belongs to.
func (w *world) slot(t time.Time) int {
	if w.window <= 0 {
		return 0
	}
	i := int(t.Sub(w.start) / w.window)
	if i < 0 {
		i = 0
	}
	if i >= len(w.winWrite) {
		i = len(w.winWrite) - 1
	}
	return i
}

func startSave(w *world, d plan) {
	w.copies = applyRetention(w.copies, d.Decision)

	kind := d.Kind
	if kind == "" {
		kind = policy.WriteFull
	}
	var cost time.Duration
	switch {
	case w.model != nil:
		coverage := time.Duration(0)
		if kind == policy.WriteDelta && !w.lastSeal.IsZero() {
			coverage = w.now.Sub(w.lastSeal)
		}
		cost = w.model.Cost(kind, coverage)
	case kind == policy.WriteDelta && w.deltaShare != nil && !w.lastSeal.IsZero():
		// A delta carries what changed since the last checkpoint, so its cost grows with the gap.
		coverage := w.now.Sub(w.lastSeal)
		if share := w.deltaShare(w.now.Sub(w.start)); share > 0 {
			cost = time.Duration(share * float64(coverage))
		}
	}
	if cost <= 0 {
		cost = w.costAt(w.now.Sub(w.start))
	}
	if cost <= 0 {
		cost = time.Second
	}
	w.saveKind = kind
	w.inFlight = true
	w.saveStart = w.now
	w.saveAt = w.now.Add(cost)
	w.willSeal = true
	if w.saveAt.After(w.end) {
		// A write cut off by the end of the trace costs what it consumed and seals nothing.
		w.saveAt = w.end
		w.willSeal = false
	}
}

func seal(w *world) {
	if !w.willSeal {
		w.truncated++
		w.inFlight = false
		return
	}
	w.lastSeal = w.saveAt
	w.inFlight = false
	w.saves++
	// The controller sees every write it ordered, not only the ones the trace happened to record.
	// Without this the samples lag by one trace cadence (half an hour) and a storm is invisible to
	// the very policy that just paid for it.
	w.writeCosts = appendWindow(w.writeCosts, w.saveAt.Sub(w.saveStart).Seconds())
	if w.saveKind != policy.WriteDelta {
		w.fullCosts = appendWindow(w.fullCosts, w.saveAt.Sub(w.saveStart).Seconds())
	}
	if w.ckpt > 0 {
		w.copies = append(w.copies, policy.Copy{
			Step:      w.step,
			Bytes:     w.ckpt,
			State:     policy.CopyVerified,
			CreatedAt: w.saveAt,
		})
	}
}

// applyRetention deletes what the decision says to delete. KeepCopies is a floor: the newest
// copies always survive, even when every copy is older than OldestStepToKeep.
func applyRetention(copies []policy.Copy, d policy.Decision) []policy.Copy {
	if len(copies) == 0 || d.KeepCopies <= 0 {
		return copies
	}
	keep := d.KeepCopies
	if keep < 1 {
		keep = 1
	}
	ordered := make([]policy.Copy, len(copies))
	copy(ordered, copies)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Step < ordered[j].Step })

	cut := len(ordered) - keep
	if cut < 0 {
		cut = 0
	}
	if d.OldestStepToKeep > 0 {
		for i := 0; i < cut; i++ {
			if ordered[i].Step >= d.OldestStepToKeep {
				cut = i
				break
			}
		}
	}
	return ordered[cut:]
}

func snapshotOf(w world) view {
	free := w.free
	if w.volume > 0 {
		used := int64(0)
		for _, c := range w.copies {
			used += c.Bytes
		}
		free = w.volume - used
	}
	s := view{
		Snapshot: policy.Snapshot{
			Step:            w.step,
			SaveProgress:    -1,
			CheckpointBytes: w.ckpt,
			FreeBytes:       free,
			Copies:          w.copies,
		},
		Now:      w.now,
		LastSeal: w.lastSeal,
	}
	if w.warnBefore > 0 {
		s.Evidence = conditionsOf(w, free)
	}
	return s
}

// conditionsOf is the replay's stand-in for a producer: it reports the conditions a deployment
// would get from the scheduler, the storage and the job itself, using only what the trace knows.
// The warning window is modelled (Options.WarnBefore), so a table built on it is an assumption
// about lead time, not a measurement of it.
func conditionsOf(w world, free int64) map[string]policy.Observation {
	at := w.now
	obs := func(v bool) policy.Observation {
		return policy.Observation{Kind: policy.Boolean, Bool: v, At: at, Source: "replay"}
	}
	out := make(map[string]policy.Observation, 3)
	horizon := w.warnBefore
	for _, k := range w.kills {
		if !k.After(w.now) {
			continue
		}
		if k.Sub(w.now) <= horizon {
			out["risk.preempt"] = obs(true)
		}
		break
	}
	if !w.end.IsZero() && w.end.Sub(w.now) <= horizon {
		out["job.ending"] = obs(true)
	}
	if w.ckpt > 0 && free >= 0 && free < 2*w.ckpt {
		out["store.low-space"] = obs(true)
	}
	return out
}

func policyPlanner(opts Options) func(time.Time, view) plan {
	return func(now time.Time, s view) plan {
		return opts.Policy.Decide(opts.Bounds, s)
	}
}

func fixedPlanner(period time.Duration) func(time.Time, view) plan {
	if period <= 0 {
		period = 10 * time.Minute
	}
	return func(now time.Time, s view) plan {
		d := plan{Decision: policy.Decision{
			Action:        policy.ActionSkip,
			Reason:        policy.ReasonSchedule,
			CopiesToWrite: 1,
			KeepCopies:    1,
		}}
		if s.LastSeal.IsZero() {
			d.Action = policy.ActionSave
			d.Reason = policy.ReasonFirst
			d.SaveNow = true
			return d
		}
		next := s.LastSeal.Add(period)
		d.NextCheck = next
		if !now.Before(next) {
			d.Action = policy.ActionSave
			d.SaveNow = true
		}
		return d
	}
}

// writeCostOf reads the write costs the trace recorded. It returns the 90th percentile (the same
// statistic the policy plans for: the bound has to survive a bad day, not an average one) and a
// curve, so a replay can charge a write what a write actually cost at that moment.
func writeCostOf(tr Trace) (time.Duration, func(time.Duration) time.Duration, bool) {
	start := time.Time{}
	var knots knots
	for _, e := range tr.Events {
		switch e.Kind {
		case KindSaveStart:
			start = e.T
		case KindSaveEnd:
			if !start.IsZero() && e.T.After(start) {
				knots = append(knots, knot{at: start.Sub(tr.Events[0].T), value: e.T.Sub(start).Seconds()})
			}
		}
	}
	if len(knots) == 0 {
		return 0, nil, false
	}

	values := make([]float64, len(knots))
	for i, k := range knots {
		values[i] = k.value
	}
	sort.Float64s(values)
	idx := (len(values)*9+9)/10 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(values) {
		idx = len(values) - 1
	}
	p90 := time.Duration(values[idx] * float64(time.Second))

	curve := func(d time.Duration) time.Duration {
		v := knots.at(d)
		if v <= 0 {
			v = values[0]
		}
		return time.Duration(v * float64(time.Second))
	}
	return p90, curve, true
}

func appendWindow(values []float64, v float64) []float64 {
	if len(values) >= 16 {
		values = values[len(values)-15:]
	}
	return append(values, v)
}

func ratio(part, whole time.Duration) float64 {
	if whole <= 0 {
		return 0
	}
	return part.Seconds() / whole.Seconds()
}

func policyName(opts Options) string {
	if opts.Policy == nil {
		return "fixed-" + opts.FixedPeriod.String()
	}
	return opts.Policy.Name()
}

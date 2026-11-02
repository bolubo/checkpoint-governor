// Package bound is the arithmetic above the controller: for one mount, what a lost-work bound
// costs, and which bound a tax budget can hold.
//
// It exists because the two things a person actually argues about are not the controller's inputs:
// "can we hold 15 minutes" and "may writes ever take more than 1% of training". Both are one
// equation, and it is exact rather than modelled because `mount` is the measured p90 of sealed
// writes (docs/product-v1.md §16), never a guess:
//
//	tax    = mount / bound        (independent of the run)
//	writes = run / bound
//
// The tax does not depend on how long the run is, which is what makes it a number a person can
// carry from one job to the next. The bound cannot be shorter than one write: a window whose lead
// is its whole span asks again immediately, and the executor already says so (internal/exec,
// "this bound cannot hold").
//
// Nothing here runs in the loop. The planner answers a question before a run; the executor's window
// keeps the promise during it. The split is deliberate: safety may not depend on arithmetic being
// right, so this package can be deleted and not one write changes.
package bound

import (
	"fmt"
	"math"
	"time"
)

// Window is one candidate as a person reads it: the bound, what a run of a given length spends
// holding it, and whether it can be held at all.
type Window struct {
	// Bound is the candidate `with-us`: the most the newest checkpoint may age.
	Bound time.Duration
	// Writes is how many window writes a run of the given length spends. Zero means fewer than one
	// window fits in the run (the end-of-training write is not counted here).
	Writes int
	// CountsWrites is false when the run's length was not given: the tax is still a price, but there
	// is no run to count windows against.
	CountsWrites bool
	// Tax is the share of the run spent writing: mount/bound, in (0, 1]. It does not depend on the
	// run, because both the writes and the run grow with it.
	Tax float64
	// Feasible is false when the bound is shorter than one write, and when nothing is known: a
	// candidate that cannot be held must never be printed as a price.
	Feasible bool
	// Known is false when the mount's write cost is unknown. Nothing is priced then, and Why says so.
	Known bool
	// Why is the sentence behind Feasible and Known: empty when the candidate is a price.
	Why string
}

// Price answers what holding one bound costs on one mount, for a run of the given length.
//
// A bound of zero is the "nothing" option — no window at all — and it is reported as such rather
// than as a zero price: exceptions only is a different promise, not a free one.
func Price(mount, run, bound time.Duration) Window {
	out := Window{Bound: bound}
	switch {
	case bound <= 0:
		out.Why = "no window: exceptions only"
		return out
	case mount <= 0:
		out.Why = "the write cost is unknown: nothing is priced"
		return out
	}
	out.Known = true
	out.Tax = float64(mount) / float64(bound)
	if bound <= mount {
		// The window would be due the moment it is armed; the executor reports this rather than
		// degrading into back-to-back writes, so the bound holds nothing and has no price.
		out.Why = fmt.Sprintf("shorter than one write (%s)", short(mount))
		return out
	}
	out.Feasible = true
	if run > 0 {
		out.Writes = int(run / bound)
		out.CountsWrites = true
	}
	return out
}

// Floor is the tightest bound a tax budget can hold, which is the same equation solved the other
// way: tax ≤ T is equivalent to bound ≥ mount/T.
//
// It is the answer to the question a platform team actually asks — "we may spend at most 1% of
// training on writes, so how often may we save?" — and it is an answer, not a recommendation: the
// caller prints it and the person decides.
//
// False means the question has no answer: the write cost is unknown, or the budget is outside
// (0, 1]. A budget of 100% is not a budget.
func Floor(mount time.Duration, tax float64) (time.Duration, bool) {
	if mount <= 0 || tax <= 0 || tax > 1 {
		return 0, false
	}
	return time.Duration(math.Ceil(float64(mount) / tax)), true
}

// String renders one candidate the way a price sheet prints it.
func (w Window) String() string {
	switch {
	case !w.Known:
		return fmt.Sprintf("%-8s unknown    — %s", short(w.Bound), w.Why)
	case !w.Feasible:
		return fmt.Sprintf("%-8s infeasible — %s", short(w.Bound), w.Why)
	case w.Bound <= 0:
		return "nothing  ·  exceptions only"
	case !w.CountsWrites:
		// The tax is the price and it does not need the run; the count does, and it was not given.
		return fmt.Sprintf("%-8s %5.1f%% of the run", short(w.Bound), w.Tax*100)
	default:
		return fmt.Sprintf("%-8s %3d writes   %5.1f%% of the run", short(w.Bound), w.Writes, w.Tax*100)
	}
}

// short is how a price sheet says a length of time: a person reads minutes and hours, not "1h0m0s".
func short(d time.Duration) string {
	switch {
	case d <= 0:
		return "0"
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Round(time.Second).Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

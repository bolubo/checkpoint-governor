package simulate

import (
	"fmt"
	"math"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// EvalBounds are the evaluation's targets: what a careful user would have asked for. They are not
// the controller's contract — that contract is two numbers and no targets (design, Appendix A)
// — but a comparison needs a yardstick, and this is it.
type EvalBounds struct {
	MaxLostWork time.Duration
	MaxTax      float64
}

// DefaultEval is one user's yardstick, applied to every family so the comparison is like for like.
func DefaultEval() EvalBounds {
	return EvalBounds{MaxLostWork: 20 * time.Minute, MaxTax: 0.08}
}

// DefaultBounds is one user's controller numbers.
func DefaultBounds() policy.Bounds {
	return policy.Bounds{
		MinCopies: 2,
	}
}

// DefaultPeriods are the fixed timers a fleet runs today.
func DefaultPeriods() []time.Duration {
	return []time.Duration{
		5 * time.Minute,
		10 * time.Minute,
		15 * time.Minute,
		20 * time.Minute,
		30 * time.Minute,
		60 * time.Minute,
	}
}

// Grid is the set of fixed periods the table prints, plus the "save every L" setting that most
// users actually choose. It does not depend on one job's bounds, because a single table must be
// able to show jobs with different bounds side by side.
func Grid() []comparator {
	periods := append([]time.Duration(nil), DefaultPeriods()...)
	sortDurations(periods)

	var ctls []comparator
	for _, p := range periods {
		ctls = append(ctls, FixedPeriod{Period: p})
	}
	ctls = append(ctls, FixedAtBound{})
	for _, n := range []int64{100, 300, 600} {
		ctls = append(ctls, FixedSteps{Steps: n})
	}
	ctls = append(ctls, Always{})
	return ctls
}

// periodOf reports the period a comparator would run at, so the scorer can treat "save every L"
// and "save every 15m" as the same kind of competitor.
func periodOf(c comparator, b policy.Bounds) (time.Duration, bool) {
	switch v := c.(type) {
	case FixedPeriod:
		return v.Period, true
	case FixedAtBound:
		if b.NoUsEvery > 0 {
			return b.NoUsEvery, true
		}
		return 0, false
	}
	return 0, false
}

func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

// Row is one trace with one result per controller.
type Row struct {
	Trace string
	// Bounds are the numbers the job was given; a fleet shares the tax budget but not the bound.
	Bounds     policy.Bounds
	Eval       EvalBounds
	Results    []Result
	BestFixed  time.Duration
	BestFixedV float64
	// WindowBest is the best fixed period for each window, chosen offline with perfect hindsight
	// about that window alone: "the number a human could pick if they re-tuned every period".
	WindowBest   []time.Duration
	WithinSpread float64
	// Gain is the violation ratio of the best fixed period divided by the policy's, at the same
	// tax budget. Infinite means the policy had none and the fixed period had some.
	Gain float64
}

// Score runs every controller on one trace and finds the best fixed period for it.
func Score(name string, tr Trace, ctls []comparator, opts Options) (Row, error) {
	b, e := opts.Bounds, opts.Eval
	row := Row{Trace: name, Bounds: b, Eval: e}
	for _, c := range ctls {
		res, err := Run(tr, Options{Bounds: b, Eval: e, Policy: c, CostAt: opts.CostAt, DeltaShare: opts.DeltaShare, Model: opts.Model, WarnBefore: opts.WarnBefore})
		if err != nil {
			return Row{}, fmt.Errorf("%s on %s: %w", c.Name(), name, err)
		}
		row.Results = append(row.Results, res)
	}

	// Best fixed: among the fixed periods inside the tax budget, the one with the least violation.
	// A period that is "best" only by breaking the budget is a different configuration, not a
	// competitor. Ties are broken by spending less tax.
	bestV := math.Inf(1)
	bestTax := math.Inf(1)
	bestAny := math.Inf(1)
	var periodAny time.Duration
	eligible := false
	for i, c := range ctls {
		period, isFixed := periodOf(c, b)
		if !isFixed {
			continue
		}
		res := row.Results[i]
		if res.Tax <= e.MaxTax+0.01 {
			if res.Violation < bestV || (res.Violation == bestV && res.Tax < bestTax) {
				bestV = res.Violation
				bestTax = res.Tax
				row.BestFixed = period
				row.BestFixedV = res.Violation
				eligible = true
			}
		}
		if res.Violation < bestAny {
			bestAny = res.Violation
			periodAny = period
		}
	}
	if !eligible && periodAny > 0 {
		row.BestFixed = periodAny
		row.BestFixedV = bestAny
	}

	row.WindowBest = bestPerWindow(ctls, row.Results, b, e)
	row.WithinSpread = spreadOf(row.WindowBest)

	if ours := findByName(row.Results, "risk-v1"); ours != nil {
		switch {
		case ours.Violation == 0 && row.BestFixedV > 0:
			row.Gain = math.Inf(1)
		case ours.Violation > 0:
			row.Gain = row.BestFixedV / ours.Violation
		default:
			row.Gain = 1
		}
	}
	return row, nil
}

func findByName(results []Result, name string) *Result {
	for i := range results {
		if results[i].Policy == name {
			return &results[i]
		}
	}
	return nil
}

// bestPerWindow picks, for each window, the fixed period that would have kept the bound most of
// that window inside the budget. A period that is best overall can still be wrong for one window,
// and that is where in-run adaptivity shows up.
func bestPerWindow(ctls []comparator, results []Result, b policy.Bounds, e EvalBounds) []time.Duration {
	if len(results) == 0 {
		return nil
	}
	n := len(results[0].WindowViolation)
	best := make([]time.Duration, n)
	taxBudget := e.MaxTax
	for w := 0; w < n; w++ {
		bestV := math.Inf(1)
		bestTax := math.Inf(1)
		for i, c := range ctls {
			period, isFixed := periodOf(c, b)
			if !isFixed {
				continue
			}
			v := results[i].WindowViolation[w]
			tax := results[i].WindowTax[w]
			if tax > taxBudget+0.01 {
				continue
			}
			if v < bestV || (v == bestV && tax < bestTax) {
				bestV = v
				bestTax = tax
				best[w] = period
			}
		}
	}
	return best
}

func spreadOf(periods []time.Duration) float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	n := 0
	for _, p := range periods {
		if p <= 0 {
			continue
		}
		v := float64(p)
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
		n++
	}
	if n == 0 || lo == 0 {
		return 0
	}
	return hi / lo
}

// Spread is how far apart the best fixed periods of a set of jobs are. It is the number that makes
// "no single number" an observation: if every job liked the same period, a constant would be fine.
func Spread(rows []Row) float64 {
	periods := make([]time.Duration, 0, len(rows))
	for _, r := range rows {
		periods = append(periods, r.BestFixed)
	}
	return spreadOf(periods)
}

// FormatGain prints the gain of the policy over the best fixed period.
func FormatGain(g float64) string {
	if math.IsInf(g, 1) {
		return "inf"
	}
	if math.IsInf(g, -1) {
		return "0"
	}
	return fmt.Sprintf("%.1fx", g)
}

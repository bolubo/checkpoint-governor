package bound

import (
	"sort"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Observed is what a directory's own history says about the number its job has been living with.
//
// It is the only honest starting point for a bound: a person who has been saving every ten minutes
// has already promised something (a ten-minute period implies a bound of period plus one write), and
// we may measure that promise instead of asking them to invent a new one. Nothing here is a
// preference or a recommendation; it is what the seals did.
type Observed struct {
	// Copies is how many sealed copies the measurement is built from.
	Copies int
	// Period is the median gap from one seal to the next: the habit.
	Period time.Duration
	// Longest is the worst staleness the job has actually lived with, which is the only bound its
	// history proves. A single long gap is not averaged away, because a bound that ignores its worst
	// day is a bound that has never been tested.
	Longest time.Duration
	// WriteCost is the 90th percentile of measured write durations; zero means unknown, and then
	// nothing is priced (product-v1 §16).
	WriteCost time.Duration
}

// Observe measures a directory's habit. False means the history is too thin to say anything: fewer
// than two sealed copies with a known time, which is exactly the state a job is in before its first
// write, and a guess there would be the number this package exists to avoid.
func Observe(copies []policy.Copy, writeCosts []float64) (Observed, bool) {
	// The write cost does not need a history: one measured write is one measured write, and a
	// directory with a fresh ledger and few copies is a real state (a job on its first run).
	out := Observed{}
	if v, ok := p90(writeCosts); ok {
		out.WriteCost = time.Duration(v * float64(time.Second))
	}

	sealed := make([]time.Time, 0, len(copies))
	for _, c := range copies {
		// A copy is evidence only once it is sealed or verified, and only when its time is known:
		// an unverified copy may be half-written, and a zero time is not a moment in the past.
		if c.State != policy.CopySealed && c.State != policy.CopyVerified {
			continue
		}
		if c.CreatedAt.IsZero() {
			continue
		}
		sealed = append(sealed, c.CreatedAt)
	}
	if len(sealed) < 2 {
		return out, false
	}
	sort.Slice(sealed, func(i, j int) bool { return sealed[i].Before(sealed[j]) })

	gaps := make([]time.Duration, 0, len(sealed)-1)
	var longest time.Duration
	for i := 1; i < len(sealed); i++ {
		gap := sealed[i].Sub(sealed[i-1])
		if gap < minGap {
			// Two seals inside the same second are one moment, not a period: counting a millisecond
			// gap as a habit would make a history of one burst look like a schedule.
			continue
		}
		gaps = append(gaps, gap)
		if gap > longest {
			longest = gap
		}
	}
	if len(gaps) == 0 {
		return out, false
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	out.Copies = len(sealed)
	out.Period = gaps[(len(gaps)-1)/2]
	out.Longest = longest
	return out, true
}

// minGap is the shortest interval that can be a period. Sealing twice within a second is one
// moment — a burst of back-to-back writes — not a cadence a person chose.
const minGap = time.Second

// p90 is the same 90th percentile internal/observe uses. It is kept local on purpose: this package
// must be a leaf so that deleting it cannot change a single write.
func p90(samples []float64) (float64, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	ordered := append([]float64(nil), samples...)
	sort.Float64s(ordered)
	idx := len(ordered) * 9 / 10
	if idx >= len(ordered) {
		idx = len(ordered) - 1
	}
	if ordered[idx] <= 0 {
		return 0, false
	}
	return ordered[idx], true
}

package permit

// Fanout is how many writes one risk is worth, and why (0022).
//
// The algorithm says whether this unit's work is at risk; that stays a boolean. How many writes the
// risk deserves is a cost comparison, and the two costs are comparable because both are measured in
// the same unit: seconds of lost progress.
type Fanout struct {
	// Writes is how many of the candidates should write. Zero means nobody: the risk does not take
	// anyone, which is case c5 of the Kueue experiment.
	Writes int
	// Reason is a short code a person can argue with.
	Reason string
	// Tax is what the writes that will not be needed cost, and Loss what a wrong pick is expected to
	// cost. Both in the same unit, so the comparison is arithmetic rather than taste.
	Tax  float64
	Loss float64
}

// Decide compares writing the whole group against writing only the expected victims.
//
//	m         candidates
//	n         expected deaths (the queue's shortfall, divided by one unit)
//	cost      what one write costs, in seconds
//	value     what one death costs, in seconds of lost progress
//	precision the chance a pick is among the dead: 1 when candidates are distinguishable, and roughly
//	          1/tieGroup when they are not
//
// The rule is `(m-n)*cost < n*value*(1-precision)`: write everyone when the tax of the unnecessary
// writes is smaller than the expected cost of guessing wrong.
func Decide(m, n int, cost, value, precision float64) Fanout {
	if n <= 0 {
		// Nobody is expected to die. This is the case that must not fire: a higher priority waiting
		// with preemption disallowed looks identical from a unit's point of view.
		return Fanout{Writes: 0, Reason: "nobody-at-risk"}
	}
	if m <= 1 {
		return Fanout{Writes: 1, Reason: "single-candidate"}
	}
	if n >= m {
		return Fanout{Writes: m, Reason: "everyone-will-die"}
	}
	if precision <= 0 || precision > 1 {
		precision = 1
	}
	if cost <= 0 || value <= 0 {
		// One side of the comparison is unknown, so the honest answer is the safe one: write the
		// group. A cost that cannot be measured must not be read as zero and silence the writes.
		return Fanout{Writes: m, Reason: "cost-unknown"}
	}

	tax := float64(m-n) * cost
	loss := float64(n) * value * (1 - precision)
	if tax < loss {
		reason := "indistinguishable"
		if precision >= 1 {
			reason = "many-will-die"
		}
		return Fanout{Writes: m, Reason: reason, Tax: tax, Loss: loss}
	}
	return Fanout{Writes: n, Reason: "only-the-victims", Tax: tax, Loss: loss}
}

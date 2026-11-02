package verify

import (
	"fmt"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Receipt is what happened to one generation, in the terms the three claims are made in (0025):
// requested, sealed, verified. It carries no fourth state and no stronger word.
type Receipt struct {
	Generation  int64
	Step        int64
	Bytes       int64
	RequestedAt time.Time
	SealedAt    time.Time
	VerifiedAt  time.Time
	// Check names the check that earned `verified`: "sample" or "full". Empty means nobody read the
	// bytes, which is why the state cannot be verified.
	Check  string
	Reason Reason
	Detail string
}

// NewReceipt builds a receipt from a verdict, and refuses the one lie it could tell.
//
// A receipt may not say `verified` unless a content check actually ran. The typing makes that hard to
// fake: there is no field to set and no constructor that skips it, and the error here is the belt to that
// braces.
func NewReceipt(v Verdict, opts Options, requested, sealed time.Time) (Receipt, error) {
	r := Receipt{
		Generation:  v.Generation,
		Step:        v.Step,
		RequestedAt: requested,
		SealedAt:    sealed,
		Reason:      v.Reason,
		Detail:      v.Detail,
	}
	if v.State == policy.CopyVerified {
		if !opts.CheckContent {
			return Receipt{}, fmt.Errorf(
				"generation %d would be recorded verified, but no content check ran", v.Generation)
		}
		r.Check = "sample"
		if opts.Full {
			r.Check = "full"
		}
		r.VerifiedAt = opts.Now
	}
	return r, nil
}

// Line renders one generation as the receipt line a person reads, or a log holds.
func (r Receipt) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "generation %d", r.Generation)
	// Everything in UTC. A receipt that mixed zones read `sealed 04:48 verified 12:48` on a CST machine —
	// eight hours between two events that happened in the same second — because the seal time comes from
	// the writer and the verify time from us.
	if !r.RequestedAt.IsZero() {
		fmt.Fprintf(&b, "  requested %s", r.RequestedAt.UTC().Format("15:04:05"))
	}
	if !r.SealedAt.IsZero() {
		fmt.Fprintf(&b, "  sealed %s", r.SealedAt.UTC().Format("15:04:05"))
	}
	if !r.VerifiedAt.IsZero() {
		fmt.Fprintf(&b, "  verified %s", r.VerifiedAt.UTC().Format("15:04:05"))
	}
	if r.Check != "" {
		fmt.Fprintf(&b, "  %s", r.Check)
	}
	if r.Step > 0 {
		fmt.Fprintf(&b, "  steps %d", r.Step)
	}
	if r.Bytes > 0 {
		fmt.Fprintf(&b, "  bytes %s", humanBytes(r.Bytes))
	}
	if r.Reason != ReasonOK && r.Reason != "" {
		fmt.Fprintf(&b, "  %s", r.Reason)
		if r.Detail != "" {
			fmt.Fprintf(&b, " (%s)", r.Detail)
		}
	}
	return b.String()
}

// Attempt renders the line that explains a rewrite: which attempt, what was wrong, and what to fix.
// "retry" is not a diagnosis, so this never says only that.
func (r Receipt) Attempt(n, of int) string {
	if r.Reason == ReasonOK {
		return ""
	}
	s := fmt.Sprintf("attempt %d of %d: %s", n, of, r.Reason)
	if r.Detail != "" {
		s += " (" + r.Detail + ")"
	}
	return s
}

// Tally is the aggregate that decides whether this product is worth running.
type Tally struct {
	Taken     int // units the scheduler took
	Verified  int // generations that verified before or after that
	Attempts  int // rewrites asked for, in total
	Invalid   int // generations that could never be verified
	Unchecked int // generations left sealed because no content check ran
}

// VerifiedPerTaken is the number the design exists to raise: of the units that died, how many had a
// verified checkpoint to come back to.
func (t Tally) VerifiedPerTaken() float64 {
	if t.Taken == 0 {
		return 0
	}
	return float64(t.Verified) / float64(t.Taken)
}

// InvalidRate must be zero. A checkpoint recorded as good and later found broken is the one failure this
// product exists to prevent, so it is reported as a rate rather than as a count to be argued about.
func (t Tally) InvalidRate() float64 {
	if total := t.Verified + t.Invalid; total > 0 {
		return float64(t.Invalid) / float64(total)
	}
	return 0
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

// Package stats summarises what a deployment has done, from the files it already keeps.
//
// It is deliberately derived rather than exported. The ledger is written whether or not anybody is
// watching, so the numbers survive a restart, a lost process, and an operator who never configured a
// scrape. A metrics endpoint would need a process that is alive to answer, which is exactly what a
// controller that just died cannot provide — and the question "how has this deployment been doing"
// is asked most often after something went wrong.
package stats

import (
	"sort"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
)

// Stats is one deployment's accounting, in the units the product already uses.
type Stats struct {
	Unit string
	// Writes is how many write costs the ledger remembers, and what they cost.
	Writes      int
	WriteTotal  float64
	WriteMedian float64
	WriteLast   float64
	// Failures counts attempts the executor gave up on; the ledger keeps it across restarts.
	Failures int
	// Reconciled counts restarts that found a plan the ledger had not recorded. A persistent
	// non-zero value means saves are being lost, which is the one number here that is a fault.
	Reconciled int
	// StepsVerified and StepsMismatched come from reading checkpoints back: the first says the
	// write was checked, the second says the check failed.
	StepsVerified   int
	StepsMismatched int
	// Units is how many step directories this deployment has seen.
	Units int
	// Plan is the last published decision, and how old it is.
	Plan      string
	PlanAge   time.Duration
	PlanKnown bool
}

// Of reads the deployment's own files and returns the summary. A deployment with no ledger is not an
// error: it is a job that has not run yet, and every count is zero.
func Of(dir string, now time.Time) (Stats, error) {
	s := Stats{Unit: dir}

	ledger, err := advise.Load(dir)
	if err != nil {
		return s, err
	}

	s.Failures = ledger.Failures
	s.Reconciled = ledger.Reconciled
	s.WriteTotal = ledger.WriteSecondsTotal
	s.Writes = len(ledger.WriteCosts)
	if s.Writes > 0 {
		s.WriteLast = ledger.WriteCosts[s.Writes-1]
		sorted := append([]float64(nil), ledger.WriteCosts...)
		sort.Float64s(sorted)
		s.WriteMedian = sorted[len(sorted)/2]
	}
	s.StepsVerified = len(ledger.VerifiedSteps())
	s.StepsMismatched = len(ledger.MismatchedSteps())
	s.Units = len(ledger.Seen())

	if published, ok := advise.ReadPublished(dir); ok {
		s.Plan = string(published.Decision.Action)
		if published.Decision.Reason != "" {
			s.Plan += " (" + string(published.Decision.Reason) + ")"
		}
		s.PlanKnown = true
		if !published.PublishedAt.IsZero() {
			s.PlanAge = now.Sub(published.PublishedAt)
		}
	}
	return s, nil
}

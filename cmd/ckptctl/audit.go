package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/audit"
	"github.com/bolubo/checkpoint-governor/internal/observe"
)

// runAudit prints the bound certificate for one directory: the run, the moment the newest copy was
// oldest, the seconds spent over the bound, and the writes that bought it.
//
// It is read-only and it exits non-zero when the bound was broken, so the claim "we held 15 minutes"
// is a check a CI job can run rather than a sentence in a README. Nothing here feeds a decision.
func runAudit(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bound := fs.Duration("bound", 0, "the bound to certify (0 = the window the ledger last ran with)")
	asJSON := fs.Bool("json", false, "print the certificate as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ckptctl audit --dir=<checkpointDir> [--bound=15m]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	dir := fs.Arg(0)
	now := time.Now()

	s, _, err := observe.Scan(observe.Request{Dir: dir, UnitID: dir, Now: now})
	if err != nil {
		fmt.Fprintf(out, "error: cannot read %s: %v\n", dir, err)
		return 1
	}
	led, err := advise.Load(dir)
	if err != nil {
		fmt.Fprintf(out, "error: cannot read the ledger in %s: %v\n", dir, err)
		return 1
	}

	// The bound is the user's number; the ledger carries the one the run last used, so an audit does
	// not need the flag when the run is still there to ask.
	certified := *bound
	source := "--bound"
	if certified <= 0 {
		certified = time.Duration(led.Status.WindowSpanSeconds) * time.Second
		source = "the window the ledger last ran with"
	}
	if certified <= 0 {
		fmt.Fprintf(out, "error: no bound to certify: pass --bound=15m, or run with a window first\n")
		return 2
	}

	start := time.Unix(led.Status.StartedAt, 0)
	if led.Status.StartedAt == 0 {
		start = time.Unix(led.UnitStartedAt, 0)
	}
	if start.IsZero() || start.Unix() <= 0 {
		// A directory with copies but no run has nothing to certify: the certificate is about the
		// interval we governed, and its start is the ledger's, not the first copy's.
		fmt.Fprintf(out, "error: this directory has no run to certify yet (no start in the ledger)\n")
		return 2
	}

	seals := make([]time.Time, 0, len(s.Copies))
	for _, c := range s.Copies {
		// The ledger's clock is whole seconds (it stores Unix times), so the copies are compared on
		// that clock too; a sub-second mtime would otherwise look like a gap before the run began.
		seals = append(seals, c.CreatedAt.Truncate(time.Second))
	}
	got := audit.Certify(certified, start, now, seals)

	if *asJSON {
		payload := auditPayload{
			Schema: 1, Dir: dir, BoundSeconds: certified.Seconds(), BoundSource: source,
			Start: start.UTC().Format(time.RFC3339), End: now.UTC().Format(time.RFC3339),
			RunSeconds: got.Run().Seconds(), Seals: got.Seals, Covered: got.Covered,
			UncoveredSeconds:    got.Uncovered().Seconds(),
			MaxStalenessSeconds: got.MaxStaleness.Seconds(),
			OverBoundSeconds:    got.OverBound.Seconds(), Held: got.Held(),
		}
		if !got.MaxAt.IsZero() {
			payload.MaxAt = got.MaxAt.UTC().Format(time.RFC3339)
		}
		writeJSON(out, payload)
		if !got.Held() {
			return 1
		}
		return 0
	}

	fmt.Fprintf(out, "audit           %s\n", dir)
	fmt.Fprintf(out, "bound           %s (from %s)\n", round(certified), source)
	fmt.Fprintf(out, "run             %s → %s (%s)\n",
		start.Local().Format("15:04:05"), now.Local().Format("15:04:05"), round(got.Run()))
	fmt.Fprintf(out, "seals           %d complete copies\n", got.Seals)
	if !got.Covered {
		fmt.Fprintf(out, "uncovered       %s with no complete copy at all (from %s)\n",
			round(got.Uncovered()), got.GapFrom.Local().Format("15:04:05"))
	}
	if got.MaxAt.IsZero() {
		fmt.Fprintf(out, "max staleness   unknown (no copy landed during this run)\n")
	} else {
		fmt.Fprintf(out, "max staleness   %s, just before the seal at %s\n",
			round(got.MaxStaleness), got.MaxAt.Local().Format("15:04:05"))
	}
	fmt.Fprintf(out, "over-bound      %s\n", round(got.OverBound))
	fmt.Fprintf(out, "writes          %s\n", writesLine(led, got.Run()))

	verdict := "HELD — no moment was older than the bound"
	if !got.Covered {
		verdict = "NOT HELD — the run began with no complete copy"
	} else if got.OverBound > 0 {
		verdict = "NOT HELD — the newest copy was older than the bound for " + round(got.OverBound)
	}
	fmt.Fprintf(out, "verdict         %s\n", verdict)
	if !got.Held() {
		return 1
	}
	return 0
}

// The JSON view is the certificate, not a second opinion; durations are seconds so a script does
// not have to parse "10m0s".
type auditPayload struct {
	Schema              int     `json:"schema"`
	Dir                 string  `json:"dir"`
	BoundSeconds        float64 `json:"boundSeconds"`
	BoundSource         string  `json:"boundSource"`
	Start               string  `json:"start"`
	End                 string  `json:"end"`
	RunSeconds          float64 `json:"runSeconds"`
	Seals               int     `json:"seals"`
	Covered             bool    `json:"covered"`
	UncoveredSeconds    float64 `json:"uncoveredSeconds,omitempty"`
	MaxStalenessSeconds float64 `json:"maxStalenessSeconds"`
	MaxAt               string  `json:"maxAt,omitempty"`
	OverBoundSeconds    float64 `json:"overBoundSeconds"`
	Held                bool    `json:"held"`
}

// writesLine prices the writes the receipt counts, when the ledger has them: a certificate that
// claims a bound without saying what it cost is half a certificate.
func writesLine(led advise.Ledger, run time.Duration) string {
	k := led.Status.Kinds
	total := k["window"] + k["exception"] + k["final"] + k["repair"]
	if total == 0 {
		return "none recorded in this directory"
	}
	line := fmt.Sprintf("%d (%d window · %d exception · %d final · %d repair)",
		total, k["window"], k["exception"], k["final"], k["repair"])
	spent := time.Duration(led.WriteSecondsTotal * float64(time.Second))
	if spent > 0 {
		line += " · " + round(spent)
		if run > 0 {
			line += fmt.Sprintf(" (%.2f%% of the run)", float64(spent)/float64(run)*100)
		}
	}
	return line
}

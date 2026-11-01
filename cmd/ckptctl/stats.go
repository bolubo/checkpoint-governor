package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/stats"
)

// runStats prints the deployment's own accounting: what it wrote, what it cost, what failed.
//
// It reads files the controller already keeps, so it works after the controller is gone — which is
// when the question is usually asked. It is not a monitoring surface and it is not a time series: it
// is one page describing one directory.
func runStats(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	dir := fs.String("dir", "", "the checkpoint directory to summarise")
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprint(out, "usage: ckptctl stats --dir=<checkpointDir> [--json]\n")
		return 2
	}

	s, err := stats.Of(*dir, time.Now())
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}

	if *asJSON {
		raw, err := json.MarshalIndent(struct {
			stats.Stats
			PlanAgeSeconds float64 `json:"planAgeSeconds"`
		}{s, s.PlanAge.Seconds()}, "", "  ")
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(out, string(raw))
		return 0
	}

	fmt.Fprintf(out, "deployment      %s\n", s.Unit)
	if s.Writes > 0 {
		fmt.Fprintf(out, "writes          %d · median %.1fs · total %.1fs · last %.1fs\n",
			s.Writes, s.WriteMedian, s.WriteTotal, s.WriteLast)
	} else {
		fmt.Fprint(out, "writes          none recorded\n")
	}
	fmt.Fprintf(out, "failures        %d\n", s.Failures)
	if s.Reconciled > 0 {
		// The one number here that is a fault: a plan found on disk that the ledger had not recorded
		// means a save was lost.
		fmt.Fprintf(out, "reconciled      %d · plans were found on disk that the ledger had not recorded\n", s.Reconciled)
	} else {
		fmt.Fprintf(out, "reconciled      %d\n", s.Reconciled)
	}
	if s.StepsVerified > 0 || s.StepsMismatched > 0 {
		fmt.Fprintf(out, "verified        %d steps · %d mismatched\n", s.StepsVerified, s.StepsMismatched)
	} else {
		fmt.Fprint(out, "verified        nothing read back yet\n")
	}
	fmt.Fprintf(out, "units           %d seen\n", s.Units)
	if s.PlanKnown {
		fmt.Fprintf(out, "plan            %s · %s old\n", s.Plan, roundDuration(s.PlanAge))
	} else {
		fmt.Fprint(out, "plan            none published\n")
	}
	return 0
}

// roundDuration keeps the summary readable: a person reading a table does not need milliseconds for
// something an hour old.
func roundDuration(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond)
	case d < time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(time.Minute)
	}
}

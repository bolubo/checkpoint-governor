package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/bound"
	"github.com/bolubo/checkpoint-governor/internal/observe"
)

// runPrice prints what one bound costs on one directory's mount, and which bound the directory's own
// history has already proven.
//
// It is read-only: one scan of the directory, one read of the ledger, then arithmetic. The controller
// never reads this output, which is the point — a price sheet that could influence a decision would
// be a second policy. See internal/bound for the equation and docs/product-v1.md §14 for who it is for.
func runPrice(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("price", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tax := fs.Float64("tax", 0.01, "the share of a run that may be spent writing, in (0,1]")
	run := fs.Duration("for", 0, "the run's length, so the sheet can count writes (0 = unknown)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ckptctl price --dir=<checkpointDir> [--tax=0.01] [--for=4h]")
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

	// The same scan the controller uses, so the price describes the directory that exists rather
	// than a copy of it kept somewhere else.
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
	hist, ok := bound.Observe(s.Copies, led.WriteCosts)

	fmt.Fprintf(out, "price           %s\n", dir)
	mount := hist.WriteCost
	if mount > 0 {
		fmt.Fprintf(out, "write cost      %s (measured: p90 of %d sealed writes)\n",
			round(mount), len(led.WriteCosts))
	} else {
		fmt.Fprintf(out, "write cost      unknown (no write has been measured in this directory)\n")
	}
	if ok {
		fmt.Fprintf(out, "your habit      period %s · worst staleness %s (%d sealed copies)\n",
			round(hist.Period), round(hist.Longest), hist.Copies)
	} else {
		fmt.Fprintf(out, "your habit      none yet (fewer than two sealed copies with a known time)\n")
	}

	// The candidates: the bound the history has already proven, plus the ladder a person would
	// otherwise have picked from a blog post.
	candidates := []time.Duration{1 * time.Minute, 5 * time.Minute, 10 * time.Minute,
		15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour}
	if ok {
		candidates = append(candidates, hist.Longest)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	candidates = dedupe(candidates)

	fmt.Fprintf(out, "\nbound     what it costs\n")
	fmt.Fprintf(out, "%-8s exceptions only — a silent death costs everything\n", "nothing")
	for _, b := range candidates {
		fmt.Fprintf(out, "%s\n", bound.Price(mount, *run, b).String())
	}

	// The question a platform team asks, solved the other way: at most this share of the run on
	// writes means never tighter than this bound on this mount.
	if floor, okay := bound.Floor(mount, *tax); okay {
		fmt.Fprintf(out, "\ntax budget %.0f%% ⇒ the tightest bound this mount can hold is %s\n",
			*tax*100, round(floor))
	} else {
		fmt.Fprintf(out, "\ntax budget %.0f%% ⇒ unreachable: the write cost is unknown, so nothing is promised\n",
			*tax*100)
	}
	return 0
}

// dedupe drops repeated candidates, so a habit that happens to equal a ladder value is printed once.
func dedupe(d []time.Duration) []time.Duration {
	out := d[:0]
	for i, v := range d {
		if i > 0 && v == d[i-1] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// round prints a duration the way a price sheet reads it: whole seconds, minutes, or hours.
func round(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Round(time.Second).Seconds())
	case d < time.Hour:
		if d%time.Minute == 0 {
			return fmt.Sprintf("%dm", int(d.Minutes()))
		}
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Round(time.Second).Seconds())%60)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

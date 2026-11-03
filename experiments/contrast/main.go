//go:build ignore

// The contrast: one command, four policies, one table. Run with `make contrast`.
//
// Why it exists, and why the numbers are a model rather than a measurement, is decision 0028.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/contrast"
)

func main() {
	d := contrast.Default()
	steps := flag.Int("steps", d.Steps, "steps in the job")
	stepMS := flag.Int("step-ms", int(d.Step/time.Millisecond), "virtual milliseconds per step")
	writeMS := flag.Int("write-ms", int(d.WriteCost/time.Millisecond), "virtual milliseconds one write costs")
	saveEvery := flag.Int("save-every-steps", d.SaveEverySteps, "the framework default: save every N steps")
	bound := flag.Duration("bound", d.Bound, "the promise a platform would make")
	flag.Parse()

	o := contrast.Options{
		Steps:          *steps,
		Step:           time.Duration(*stepMS) * time.Millisecond,
		WriteCost:      time.Duration(*writeMS) * time.Millisecond,
		SaveEverySteps: *saveEvery,
		Bound:          *bound,
	}
	rows, err := contrast.Run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "contrast:", err)
		os.Exit(1)
	}

	fmt.Printf("a %s job, %d steps, one write costs %s — a model, not a measurement\n",
		contrast.Short(time.Duration(*steps)*o.Step), *steps, contrast.Short(o.WriteCost))
	fmt.Println()
	fmt.Printf("%-28s %8s %10s  %-24s\n", "policy", "writes", "io", "worst case, no signal")
	for _, r := range rows {
		fmt.Printf("%-28s %8d %10s  %-24s\n",
			r.Policy, r.Writes, contrast.Short(r.IO), r.NoSignal)
	}
	fmt.Println()
	fmt.Println("this job has no signals: a stop that announces itself is risk.shutdown, and the executor covers it")
	fmt.Println("one probe interval plus one round later — that budget is in docs/conditions.md, not in this model")
}

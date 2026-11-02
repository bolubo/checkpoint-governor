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
	fmt.Printf("%-28s %8s %10s  %-16s %-16s\n", "policy", "writes", "io", "announced stop", "silent death")
	for _, r := range rows {
		fmt.Printf("%-28s %8d %10s  %-16s %-16s\n",
			r.Policy, r.Writes, contrast.Short(r.IO), r.Announced, r.Silent)
	}
	fmt.Println()
	fmt.Println("every row names what it covers; a row with a count and no coverage would be refused")
}

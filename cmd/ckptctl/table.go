package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/simulate"
)

// runTable prints the evidence table: every family under the controllers worth comparing.
//
// It is one command on purpose. A table that needs a second binary or a notebook is a table nobody
// re-runs, and a claim nobody re-runs stops being checkable.
func runTable(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("table", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	seeds := fs.Int("seeds", 2, "how many seeds per family")
	dir := fs.String("out", "", "write the generated traces into this directory")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ckptctl table [--seeds=2] [--out=DIR]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctls := simulate.Grid()
	risk, ok := policy.Lookup("risk-v1")
	if !ok {
		fmt.Fprintln(os.Stderr, "the risk policy is missing from the registry")
		return 1
	}
	ctls = append(ctls, simulate.Boundary(risk))

	var rows []simulate.Row
	for _, fam := range simulate.Families() {
		bounds, eval := simulate.BoundsFor(fam)
		for seed := int64(1); seed <= int64(*seeds); seed++ {
			trace, env, err := simulate.Generate(seed, fam)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s seed %d: %v\n", fam, seed, err)
				return 1
			}
			name := fmt.Sprintf("%s-%d", fam, seed)
			if *dir != "" {
				if err := writeTrace(*dir, name, trace); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
			}
			row, err := simulate.Score(name, trace, ctls, simulate.Options{
				Bounds:     bounds,
				Eval:       eval,
				CostAt:     env.WriteCost,
				DeltaShare: env.DeltaShare,
				Model:      env.Model,
				// The replay stands in for a producer: a kill is visible this long before it
				// happens, and the end of the trace is visible the same way. It is a modelled
				// warning window (decision 0014 §8); the real one has to be measured.
				WarnBefore: 90 * time.Second,
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			rows = append(rows, row)
		}
	}

	fmt.Fprintf(out, "risk-v1 is replayed with a modelled 90s warning before each recorded kill and before the end of the trace\n")
	fmt.Fprintf(out, "columns: violation%% / tax%% / writes; risk-v1 writes only when a condition fires\n\n")
	simulate.PrintTable(out, rows, ctls)
	return 0
}

func writeTrace(dir, name string, tr simulate.Trace) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, name+".jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	return simulate.Encode(tr, f)
}

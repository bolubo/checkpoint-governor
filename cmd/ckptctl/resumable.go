package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// runResumable answers "can this be resumed", from the declaration beside the manifest.
//
// It is the question `verify` cannot answer: a copy can be whole and still unable to continue the run,
// because the library saved only the weights or because the world size changed under a format that
// cannot re-shard (decision 0026). Exit codes keep the product's three meanings: 0 yes, 1 checked and no,
// 2 cannot decide.
func runResumable(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("resumable", flag.ContinueOnError)
	dir := fs.String("dir", "", "the checkpoint directory")
	world := fs.Int("world", 0, "the world size a restore would run under (0 = the checkpoint's own)")
	tp := fs.Int("tp", 0, "tensor-parallel size a restore would use (0 = not asked)")
	pp := fs.Int("pp", 0, "pipeline-parallel size a restore would use (0 = not asked)")
	dp := fs.Int("dp", 0, "data-parallel size a restore would use (0 = not asked)")
	gen := fs.Int64("generation", 0, "the generation to judge (0 = the newest sealed)")
	asJSON := fs.Bool("json", false, "print the verdict as JSON")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprint(out, "usage: ckptctl resumable --dir=<checkpointDir> [--world=N] [--generation=N]\n")
		return 2
	}

	sealed, ok, err := resume.NewestSealed(*dir)
	if err != nil {
		fmt.Fprintf(out, "unknown: cannot list generations: %v\n", err)
		return 2
	}
	if *gen != 0 {
		sealed, ok, err = resume.Generation(*dir, *gen)
		if err != nil {
			fmt.Fprintf(out, "unknown: cannot list generations: %v\n", err)
			return 2
		}
	}
	if !ok {
		// No sealed copy is not a bad copy: there is nothing to restore from yet, and saying "not
		// resumable" would blame the writer for a write that has not finished.
		fmt.Fprintln(out, "unknown: nothing sealed yet")
		return 2
	}

	f, declared, loadErr := resume.Load(sealed.Dir)
	asked := resume.World{Ranks: *world, TP: *tp, PP: *pp, DP: *dp}
	v := resume.Judge(f, declared, loadErr, asked)

	if *asJSON {
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			fmt.Fprintf(out, "unknown: cannot encode the verdict: %v\n", err)
			return 2
		}
		fmt.Fprintf(out, "%s\n", raw)
	} else {
		fmt.Fprintln(out, v.Line())
	}
	return v.ExitCode()
}

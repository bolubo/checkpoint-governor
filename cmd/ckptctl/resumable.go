package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/bolubo/checkpoint-governor/internal/layout"
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

	asked := resume.World{Ranks: *world, TP: *tp, PP: *pp, DP: *dp}
	cfg, _, cfgErr := layout.LoadConfig(*dir)
	if cfgErr != nil {
		fmt.Fprintf(out, "unknown: %v\n", cfgErr)
		return 2
	}

	var v resume.Verdict
	if *gen != 0 {
		// `--generation` names one of our own sealed copies. A framework checkpoint has no generation
		// number to name; asking for one is a misreading, and saying so beats picking some copy.
		res, err := layout.Scan(*dir, cfg)
		if err != nil {
			fmt.Fprintf(out, "unknown: cannot list copies: %v\n", err)
			return 2
		}
		if !res.Found || res.Newest.Kind != layout.KindGen {
			fmt.Fprintf(out, "unknown: --generation is only for gen-* copies (newest is %s, a %s)\n",
				res.Newest.Artifact(), res.Newest.Kind)
			return 2
		}
		sealed, ok, err := resume.Generation(*dir, *gen)
		if err != nil {
			fmt.Fprintf(out, "unknown: cannot list generations: %v\n", err)
			return 2
		}
		if !ok {
			fmt.Fprintf(out, "unknown: gen %d is not sealed\n", *gen)
			return 2
		}
		v = layout.Judge(layout.Copy{Dir: sealed.Dir, Target: ".", Step: *gen, Kind: layout.KindGen, Sealed: true}, cfg, asked)
	} else {
		res, err := layout.Scan(*dir, cfg)
		if err != nil {
			fmt.Fprintf(out, "unknown: cannot list copies: %v\n", err)
			return 2
		}
		if !res.Found && res.Unsealed == nil {
			// No copy is not a bad copy: there is nothing to restore from yet, and saying "not
			// resumable" would blame the writer for a write that has not started.
			fmt.Fprintln(out, "unknown: nothing sealed yet")
			return 2
		}
		v = res.Verdict(cfg, asked)
	}

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

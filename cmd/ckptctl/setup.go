package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/bolubo/checkpoint-governor/internal/storage"
)

// runSetup prepares a deployment. In this milestone it has one mode, --local, which needs no cluster
// and no kubectl: it makes the directory, measures what the volume promises, and prints the four
// lines of glue plus the next command. The cluster check/write modes are 0.28's later milestone and
// live in the same command so a user has one entry point.
func runSetup(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(out)
	local := fs.Bool("local", false, "prepare one checkpoint directory on this machine (no cluster)")
	dir := fs.String("dir", "", "the checkpoint directory")
	fs.Usage = func() {
		fmt.Fprintln(out, "usage: ckptctl setup --local --dir=<checkpointDir>")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*local || *dir == "" {
		fs.Usage()
		return 2
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(out, "setup: cannot create %s: %v\n", *dir, err)
		return 1
	}
	r, err := storage.Probe(*dir)
	if err != nil {
		fmt.Fprintf(out, "setup: blocked — %v\n", err)
		return 1
	}

	fmt.Fprintf(out, "setup %s: local\n", *dir)
	fmt.Fprintf(out, "volume      %s (%s), promise %s\n", r.Class, r.FSName, r.Promise)
	if !r.Safe() {
		fmt.Fprintf(out, "warning     this volume promises nothing beyond what is readable now; do not resume across pods from it\n")
	}
	fmt.Fprintf(out, "next        ckptctl init --dir=%s --duration=4h\n", *dir)
	fmt.Fprintf(out, "loop        g = Governor(%q, start_engine=True)\n", *dir)
	fmt.Fprintln(out, "            for step in ...: g.step(step)")
	fmt.Fprintln(out, "            if g.requested(): save(); g.sealed(gen, step, shards)")
	fmt.Fprintln(out, "            g.ending(); g.close()")
	return 0
}

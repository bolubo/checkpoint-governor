package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// runResumed answers "did it resume", from the job's own report and the seal that existed when it
// started.
//
// It is the sentence that separates a restart from a resume: a process that came back and started at
// step 0 is `not adopted`, and no amount of whole bytes changes that (decision 0026).
func runResumed(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("resumed", flag.ContinueOnError)
	dir := fs.String("dir", "", "the checkpoint directory")
	asJSON := fs.Bool("json", false, "print the verdict as JSON")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprint(out, "usage: ckptctl resumed --dir=<checkpointDir>\n")
		return 2
	}

	a, err := resume.Observe(*dir)
	if err != nil {
		fmt.Fprintf(out, "unknown: cannot read the directory: %v\n", err)
		return 2
	}
	if *asJSON {
		raw, err := json.MarshalIndent(a, "", "  ")
		if err != nil {
			fmt.Fprintf(out, "unknown: cannot encode the verdict: %v\n", err)
			return 2
		}
		fmt.Fprintf(out, "%s\n", raw)
	} else {
		fmt.Fprintln(out, a.Line())
	}
	return a.ExitCode()
}

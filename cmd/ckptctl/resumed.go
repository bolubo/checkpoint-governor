package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bolubo/checkpoint-governor/internal/layout"
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
	if a.Reason == resume.AdoptNone {
		// No gen-* manifest: ask the finder, so a framework writer is not told its own checkpoints do
		// not exist. A copy written by this run (after boot) is not something it could have adopted.
		if step, ok := newestBeforeBoot(*dir); ok {
			if observed, err := resume.ObserveStep(*dir, step); err == nil {
				a = observed
			}
		}
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

// newestBeforeBoot is the newest framework copy that existed when this run started. Newest-alone is
// wrong here: after a resume the run may have saved a later checkpoint itself, and calling those steps
// "unused" would blame the job for the work it just did.
func newestBeforeBoot(dir string) (int64, bool) {
	cfg, _, err := layout.LoadConfig(dir)
	if err != nil {
		return 0, false
	}
	res, err := layout.Scan(dir, cfg)
	if err != nil {
		return 0, false
	}
	boot, hasBoot := resume.BootTime(dir)
	for _, c := range res.Sealed {
		if c.Kind == layout.KindGen {
			continue // Observe already looked for a manifest of ours
		}
		if hasBoot {
			path := c.Dir
			if c.Target != "" && c.Target != "." {
				path = filepath.Join(c.Dir, c.Target)
			}
			info, err := os.Stat(path)
			if err != nil || info.ModTime().After(boot) {
				continue
			}
		}
		return c.Step, true
	}
	return 0, false
}

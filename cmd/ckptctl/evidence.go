package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/bolubo/checkpoint-governor/internal/evidence"
)

// runEvidence prints one versioned bundle: what the directory is doing, whether it can resume, what
// the newest copy is worth, what was attempted, and what the volume promises. It is the machine view
// that `status`, `resumable`, `verify` and `doctor --storage` already agree on, assembled rather than
// re-decided.
func runEvidence(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("evidence", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "the checkpoint directory")
	asJSON := fs.Bool("json", false, "print the bundle as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(out, "usage: ckptctl evidence --dir=<checkpointDir> [--json]")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fs.Usage()
		return 2
	}
	if _, err := os.Stat(*dir); err != nil {
		fmt.Fprintf(out, "evidence: cannot read %s: %v\n", *dir, err)
		return 1
	}

	b := evidence.Collect(*dir)
	if *asJSON {
		raw, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			fmt.Fprintf(out, "evidence: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "%s\n", raw)
		return 0
	}

	fmt.Fprintf(out, "evidence %s: schema %d\n", b.Dir, b.Schema)
	fmt.Fprintf(out, "status      generation %d · step %d", b.Status.Generation, b.Status.Step)
	if b.Status.Phase != "" {
		fmt.Fprintf(out, " · phase %s", b.Status.Phase)
	}
	fmt.Fprintln(out)
	if b.Resume.Detail != "" || b.Resume.State != "" {
		fmt.Fprintf(out, "resume      %s\n", resumeSentence(b))
	}
	if b.Newest != nil {
		fmt.Fprintf(out, "verify      %s: %s\n", b.Newest.State, b.Newest.Detail)
	} else {
		fmt.Fprintln(out, "verify      none: no sealed generation")
	}
	if b.Storage != nil {
		if b.Storage.Class == "unknown" {
			fmt.Fprintf(out, "storage     unknown: %s\n", b.Storage.Detail)
		} else {
			fmt.Fprintf(out, "storage     %s (%s), %s\n", b.Storage.Class, b.Storage.FS, b.Storage.Promise)
		}
	}
	fmt.Fprintf(out, "problems    %d\n", len(b.Problems))
	for _, p := range b.Problems {
		fmt.Fprintf(out, "            %s: %s\n", p.Code, p.Detail)
	}
	if len(b.Attempts) > 0 {
		fmt.Fprintf(out, "attempts    %d\n", len(b.Attempts))
	}
	return 0
}

func resumeSentence(b evidence.Bundle) string {
	if b.Resume.Line != "" {
		return b.Resume.Line
	}
	switch b.Resume.State {
	case "not resumable":
		if b.Resume.Detail != "" {
			return "not resumable: " + b.Resume.Detail
		}
		return "not resumable"
	case "resumable":
		return fmt.Sprintf("resumable (gen %d, step %d, world %d/%d)",
			b.Resume.Generation, b.Resume.Step, b.Resume.World.Ranks, b.Resume.World.Ranks)
	default:
		if b.Resume.Detail != "" {
			return "unknown: " + b.Resume.Detail
		}
		return "unknown"
	}
}

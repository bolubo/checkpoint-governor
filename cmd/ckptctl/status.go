package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
)

// runStatus prints what the controller knows about one directory.
//
// It reads the private ledger and nothing else: the ledger is the controller's memory, so a stopped
// controller still explains its last state. The bound is a flag because it is the user's number and
// the ledger deliberately does not store configuration.
func runStatus(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "print the status as one JSON object (for scripts)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: ckptctl status <checkpointDir>")
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
	l, err := advise.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if l.Status.Phase == "" {
		fmt.Fprintf(out, "no status in %s: this directory has not been governed yet\n", dir)
		return 0
	}
	if *asJSON {
		// One object, no prose: a script must be able to ask the controller what it thinks without
		// parsing a table whose columns are a design choice. The resume verdict rides beside the
		// status rather than inside it: the ledger does not own the declaration (0026).
		payload := struct {
			advise.Status
			Resume string `json:"resume,omitempty"`
		}{l.Status, strings.TrimPrefix(ResumeLine(dir), "resume: ")}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(out, string(raw))
		return 0
	}
	advise.Render(out, l.Status, time.Now(), 0)
	worldSummaryLine(out, dir, time.Now())

	// "Are the bytes whole" and "can this continue" are two questions (0026); status answers both.
	if line := ResumeLine(dir); line != "" {
		fmt.Fprintln(out, line)
	}

	// What the policy could not measure is part of the answer: a person reading a plan has to know
	// which of its inputs were absent.
	if d, ok := advise.LastDecision(dir); ok {
		fmt.Fprintf(out, "write           %s\n", advise.OutputLine(d))
		if len(d.Missing) > 0 {
			fmt.Fprintf(out, "missing         %s\n", strings.Join(d.Missing, ", "))
		}
	} else if problem := advise.SchemaProblem(dir); problem != "" {
		fmt.Fprintf(out, "plan            %s\n", problem)
	}

	// Fields this build does not know (reasons, sensitivity, whatever the decision gains next) are
	// rendered as they appear in the file, so a new structured explanation needs no release here.
	if extras, ok := advise.Extras(dir); ok {
		advise.RenderExtras(out, extras)
	}
	return 0
}

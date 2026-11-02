package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/drill"
	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// runDrill proves a checkpoint can be resumed: it runs a loader for one step and compares the step it
// reports with the sealed one (decision 0026).
//
// A drill that changes the checkpoint is refused rather than passed, and a loader declared by the
// checkpoint's own file is not executed unless the person running the drill says so: a file we treat as
// untrusted must not be able to name a command.
func runDrill(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("drill", flag.ContinueOnError)
	dir := fs.String("dir", "", "the checkpoint directory")
	generation := fs.Int64("generation", 0, "the generation to drill (0 = the newest sealed)")
	loader := fs.String("loader", "", "the command to run for one step (prints step=<n>)")
	trust := fs.Bool("trust-declared-loader", false, "run the loader named in resume.json, which is untrusted input")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long the loader may run")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprint(out, "usage: ckptctl drill --dir=<checkpointDir> [--generation=N] --loader=\"<command>\"\n")
		return 2
	}

	sealed, ok, err := resume.NewestSealed(*dir)
	if err != nil {
		fmt.Fprintf(out, "drill refused: cannot list generations: %v\n", err)
		return 2
	}
	if *generation != 0 {
		sealed, ok, err = resume.Generation(*dir, *generation)
		if err != nil {
			fmt.Fprintf(out, "drill refused: cannot list generations: %v\n", err)
			return 2
		}
	}
	if !ok {
		fmt.Fprintln(out, "drill refused: nothing sealed to drill")
		return 2
	}

	command := *loader
	if command == "" && *trust {
		if f, declared, err := resume.Load(sealed.Dir); err == nil && declared {
			command = f.Loader
		}
	}
	if command == "" {
		if *trust {
			fmt.Fprintln(out, "drill refused: the declaration names no loader")
		} else {
			fmt.Fprintln(out, "drill refused: no loader given (pass --loader=…, or --trust-declared-loader)")
		}
		return 2
	}
	if *loader == "" {
		// The command came from the checkpoint, and the receipt has to say so: a person reading it later
		// must be able to tell which half of the evidence was ours.
		fmt.Fprintln(out, "using a command declared by an unverified file")
	}

	rep, err := drill.Do(context.Background(), *dir, sealed.Dir, sealed.Manifest.Generation,
		sealed.Manifest.Step, command, *timeout)
	if err != nil {
		fmt.Fprintf(out, "drill failed: %v\n", err)
		return 1
	}
	if *asJSON {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(out, "drill failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "%s\n", raw)
	} else {
		fmt.Fprintln(out, rep.Line())
	}
	return rep.ExitCode()
}

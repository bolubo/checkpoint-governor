package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bolubo/checkpoint-governor/internal/resume"
)

// runDeclare writes the statement a restore is judged against, beside a checkpoint somebody else wrote.
//
// It is the manual half of the glue: an integration calls `Governor.declare` on every save, but a job
// that saved before the integration was installed already has copies, and this command is how a
// person says what those copies hold without rewriting them (decision 0026).
func runDeclare(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("declare", flag.ContinueOnError)
	path := fs.String("dir", "", "the checkpoint directory, or the artifact file itself")
	step := fs.Int64("step", 0, "the step this copy reached")
	requires := fs.String("requires", "model,optimizer,scheduler,rng", "comma list of states a resume needs")
	state := fs.String("state", "model,optimizer,scheduler,rng", "comma list of states the copy holds")
	format := fs.String("format", resume.FormatOther, "the writer's format (dcp, torch_dist, safetensors, hf-trainer, other)")
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" || *step <= 0 {
		fmt.Fprint(out, "usage: ckptctl declare --dir=<checkpointDir|artifact> --step=N [--requires=...] [--state=...]\n")
		return 2
	}

	held, err := stateSet(*state)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}
	want, err := stateSet(*requires)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}

	isDir, err := isDir(*path)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}
	decl := resume.File{
		Schema:     resume.Schema,
		Generation: *step,
		Step:       *step,
		Format:     *format,
		World:      resume.World{Ranks: 1},
		State:      held,
		Requires:   requiresOf(want),
	}
	var dir, file string
	if isDir {
		dir, file, decl.Target = *path, "resume.json", "."
	} else {
		dir, file = filepath.Dir(*path), filepath.Base(*path)+".resume.json"
		decl.Target = filepath.Base(*path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}
	if err := resume.Write(dir, decl); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}
	if file != "resume.json" {
		if err := os.Rename(filepath.Join(dir, "resume.json"), filepath.Join(dir, file)); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(out, "declared %s (step %d, requires %s)\n", file, *step, strings.Join(decl.Requires, ", "))
	return 0
}

// stateSet turns a comma list into a State. Every name must be one the declaration can carry: an
// unknown name is a typo, and a typo that is silently dropped is worse than a refusal.
func stateSet(list string) (resume.State, error) {
	var s resume.State
	for _, name := range splitList(list) {
		switch name {
		case "model":
			s.Model = true
		case "optimizer":
			s.Optimizer = true
		case "scheduler":
			s.Scheduler = true
		case "rng":
			s.RNG = true
		case "dataloader":
			s.Dataloader = true
		case "scaler":
			s.Scaler = true
		case "ema":
			s.EMA = true
		default:
			return resume.State{}, fmt.Errorf("%q is not a state this declaration can name", name)
		}
	}
	return s, nil
}

// requiresOf keeps the catalogue's order, so two declarations made from the same set are byte-identical.
func requiresOf(s resume.State) []string {
	var out []string
	for _, e := range []struct {
		name string
		held bool
	}{
		{"model", s.Model}, {"optimizer", s.Optimizer}, {"scheduler", s.Scheduler},
		{"rng", s.RNG}, {"dataloader", s.Dataloader}, {"scaler", s.Scaler}, {"ema", s.EMA},
	} {
		if e.held {
			out = append(out, e.name)
		}
	}
	// `requires` is optional in the file; an empty list would mean "nothing needed", which is never
	// what a person means. Omitting it keeps the reader's default five.
	if len(out) == 0 {
		return nil
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

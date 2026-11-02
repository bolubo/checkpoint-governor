//go:build ignore

// Command gen-resume-fixtures writes the declaration one scenario leaves beside a sealed manifest.
//
// It is run by hack/gen-resume-fixtures.sh, and only after the real trainer has written the shards and
// the manifest: the checkpoint bytes must never be authored by hand, or the fixtures would be testing a
// writer that does not exist. The declaration itself goes through the reference writer, so a fixture
// cannot drift from the schema.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bolubo/checkpoint-governor/internal/resume"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

func main() {
	gen := flag.String("gen", "", "the generation directory holding a sealed manifest")
	profile := flag.String("profile", "", "full|weights-only|partial-optimizer|reshardable|unknown-format")
	flag.Parse()
	if *gen == "" || *profile == "" {
		fmt.Fprintln(os.Stderr, "usage: go run hack/gen-resume-fixtures.go -gen=<gen dir> -profile=<name>")
		os.Exit(2)
	}

	// The generation and step come from the manifest the real writer sealed, so the declaration cannot
	// describe a checkpoint that is not there.
	raw, err := os.ReadFile(filepath.Join(*gen, "manifest.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", *gen, err)
		os.Exit(1)
	}
	m, err := verify.Parse(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest: %v\n", err)
		os.Exit(1)
	}

	f := resume.File{
		Schema:     resume.Schema,
		Generation: m.Generation,
		Step:       m.Step,
		Format:     resume.FormatDCP,
		World:      resume.World{Ranks: 8, TP: 1, PP: 1, DP: 8},
		State: resume.State{
			Model: true, Optimizer: true, Scheduler: true,
			RNG: true, Dataloader: true,
		},
	}

	switch *profile {
	case "full":
		// everything a full restore needs, world unchanged
	case "weights-only":
		// The library's default: last checkpoint holds the weights and nothing else.
		f.State = resume.State{Model: true}
	case "partial-optimizer":
		// The file holds the optimizer, and the loader is told to skip it (a declared fine-tune).
		f.Partial = []string{"optimizer"}
	case "reshardable":
		// The layout can be read under another world size (load-time re-sharding).
		f.World.Reshardable = true
	case "unknown-format":
		// A writer that invented a format. Written without the schema's validation on purpose: this
		// fixture exists to prove the reader refuses it rather than guesses.
		f.Format = "nemo"
		b, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(*gen, resume.FileName), append(b, '\n'), 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("resume.json: %s (gen %d, step %d)\n", *profile, m.Generation, m.Step)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown profile %q\n", *profile)
		os.Exit(2)
	}

	if err := resume.Write(*gen, f); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("resume.json: %s (gen %d, step %d)\n", *profile, m.Generation, m.Step)
}

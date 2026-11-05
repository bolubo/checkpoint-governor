package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/observe"
	"github.com/bolubo/checkpoint-governor/internal/produce"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
	"github.com/bolubo/checkpoint-governor/internal/storage"
)

// runDoctor asks every probe what it sees on this machine, and prints the answer with the two things
// a person needs: why, and what would change it.
//
// It exists because the hardest question in this product is "is it watching me, or is it blind?" —
// and the honest answer is a table, not a paragraph.
func runDoctor(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	dir := fs.String("dir", "", "the checkpoint directory to inspect")
	storageDir := fs.String("storage", "", "also measure what this directory's filesystem promises")
	freeFactor := fs.Float64("free-factor", 2, "free space below this multiple of one checkpoint is at risk")
	storeEvery := fs.Duration("store-every", 5*time.Second, "how often the storage conditions are read")
	slowFactor := fs.Float64("slow-factor", 2, "how many times the job baseline counts as slow")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" && *storageDir == "" {
		fmt.Fprint(out, "usage: ckptctl doctor --dir=<checkpointDir> [--storage=<dir>]\n")
		return 2
	}

	// The storage table is printed first because it answers the question every other row depends on: on
	// this volume, does "write, sync, rename" mean what the controller's decisions assume? It can be run
	// alone, without a checkpoint directory, which is what an operator does before deploying (0027).
	storageCode := 0
	if *storageDir != "" {
		r, err := storage.Probe(*storageDir)
		if err != nil {
			fmt.Fprintf(out, "storage %s\nerror: cannot probe it: %v\n", *storageDir, err)
			return 2
		}
		r.Write(out)
		if !r.Safe() {
			storageCode = 1
		}
		if *dir == "" {
			return storageCode
		}
		fmt.Fprintln(out)
	}

	now := time.Now()

	// The plan's schema comes first, because it is the one fact that decides whether the rest of this
	// page describes the directory the binary can actually govern. A plan from another version is
	// refused with its number named — the same sentence `status` and `explain` print — and the exit
	// code says so, so a script cannot mistake a table for an answer.
	problem := ""
	if schema, ok := advise.PlanSchema(*dir); ok {
		fmt.Fprintf(out, "plan schema %d, this binary reads %s\n", schema, advise.Reads())
	} else {
		fmt.Fprintf(out, "plan schema: none (this binary reads %s)\n", advise.Reads())
	}
	problem = advise.SchemaProblem(*dir)

	// The checkpoint size is what the storage rule is measured against, so the doctor reads it the
	// same way the loop does rather than guessing.
	s, _, err := observe.Scan(observe.Request{Dir: *dir, UnitID: *dir, Now: now})
	if err != nil {
		fmt.Fprintf(out, "error: cannot read %s: %v\n", *dir, err)
		return 1
	}
	env := produce.Env{Dir: *dir, Now: now, CheckpointBytes: s.CheckpointBytes}

	// The doctor must see the same numbers the controller would: a threshold the file changed and
	// the doctor ignored would make the table a lie.
	cfg, _, err := adapters.LoadConfig(*dir)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}
	// The operator's declaration decides whether a condition exists at all, so the doctor must read
	// it before it builds the probes — otherwise the table describes a deployment nobody configured.
	if cfg.Deployment, err = adapters.LoadDeployment(*dir); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "free-factor":
			cfg.FreeSpaceFactor = *freeFactor
		case "store-every":
			cfg.StoreEvery = *storeEvery
		case "slow-factor":
			cfg.SlowFactor = *slowFactor
		}
	})
	cfg = cfg.WithWorld(*dir)
	probes := adapters.All(cfg)
	var ok, absent, unknown int
	for _, p := range probes {
		r := p.Read(env)
		state, detail := describe(r)
		// A condition whose answer is a declaration, not a measurement, says so in its own words.
		if n, ok := capabilityNote(*dir, p.Name()); ok && r.State == produce.Absent {
			detail = n
		}
		switch r.State {
		case produce.Absent:
			absent++
		case produce.Unknown:
			unknown++
		default:
			ok++
		}
		fmt.Fprintf(out, "%-24s %-8s every %-6s %s\n", p.Name(), state, p.Every(), detail)
	}

	fmt.Fprintf(out, "\ndoctor: %d probes · %d reading, %d absent, %d unknown\n", len(probes), ok, absent, unknown)
	if s.CheckpointBytes <= 0 {
		fmt.Fprintln(out, "note: no complete checkpoint yet, so the storage rule has no scale to measure against")
	}
	// The environment table above is still true, so it is printed; the refusal is the last line because
	// it is the one a person has to act on.
	if problem != "" {
		fmt.Fprintf(out, "\ndoctor: refused — %s\n", problem)
		return 2
	}
	// A storage tier that promises nothing still lets the environment table print, but the exit code
	// carries the verdict to a script: doctor told you not to trust this volume.
	return storageCode
}

// capabilityNote maps a condition to the declaration that decides whether it exists at all.
func capabilityNote(dir, condition string) (string, bool) {
	switch condition {
	case "risk.preempt":
		return adapters.CapabilityNote(dir, adapters.CapPreemption)
	case "risk.eviction-likely":
		return adapters.CapabilityNote(dir, adapters.CapEviction)
	}
	return "", false
}

// describe turns a reading into the two words the table needs.
func describe(r produce.Reading) (state, detail string) {
	switch r.State {
	case produce.Absent:
		return "absent", "this environment does not have it"
	case produce.Unknown:
		if r.Err != nil {
			return "unknown", "cannot read it: " + r.Err.Error()
		}
		return "unknown", "cannot read it"
	default:
		if r.Value {
			return "1", "firing"
		}
		return "0", "not firing"
	}
}

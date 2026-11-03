package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// runGC is the evidence half of fail-open. A hold with a past deadline no longer stops anything —
// ReadHeldReclaim ignores it — but it still says "a gate wrote this and never returned". Marking it
// `uncovered` is what lets a person see the promise that was not kept after a kill -9.
func runGC(args []string) int {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	worldDir := fs.String("world", "", "the world directory the observer writes (required)")
	cluster := fs.String("cluster", "", "the cluster name the observer writes under (required)")
	dryRun := fs.Bool("dry-run", false, "print what would be marked, change nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *worldDir == "" || *cluster == "" {
		fmt.Fprintln(os.Stderr, "ckptgate gc: --world and --cluster are required")
		return 2
	}
	dir := filepath.Join(*worldDir, *cluster, "reclaims")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("gc         no reclaims")
			return 0
		}
		fmt.Fprintf(os.Stderr, "ckptgate gc: %v\n", err)
		return 1
	}
	now := time.Now()
	marked := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ckptgate gc: %v\n", err)
			continue
		}
		var r adapters.ReclaimRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			fmt.Fprintf(os.Stderr, "ckptgate gc: %s: %v\n", e.Name(), err)
			continue
		}
		if r.State != adapters.ReclaimHeld || r.Deadline.IsZero() || now.Before(r.Deadline) {
			continue
		}
		fmt.Printf("gc         %s held for %s: past its deadline, never released\n", r.UnitID, r.Intruder)
		if *dryRun {
			continue
		}
		r.State = adapters.ReclaimUncovered
		if err := adapters.WriteReclaim(*worldDir, *cluster, r); err != nil {
			fmt.Fprintf(os.Stderr, "ckptgate gc: write %s: %v\n", r.UnitID, err)
			continue
		}
		marked++
	}
	fmt.Printf("gc         %d marked uncovered\n", marked)
	return 0
}

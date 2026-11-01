package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
	"github.com/bolubo/checkpoint-governor/internal/supervisor"
	"github.com/bolubo/checkpoint-governor/internal/unit"
)

// runSupervise drives one supervisor over several checkpoint directories and rolls the result up.
//
// It exists because the unit of work is not the process: a node runs many jobs, they share volumes,
// and a person watching them wants one line, not N terminals. The rollup is deliberately thin — it
// reads what each loop published rather than re-deciding anything.
func runSupervise(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	members := fs.String("members", "", "comma-separated checkpoint directories")
	membersFrom := fs.String("members-from", "", "a root whose immediate children are checkpoint directories")
	rounds := fs.Int("rounds", 6, "how many ticks to run")
	forever := fs.Bool("forever", false, "run until SIGTERM or SIGINT instead of --rounds")
	every := fs.Duration("every", 200*time.Millisecond, "between ticks")
	freeFactor := fs.Float64("free-factor", 2, "free space below this multiple of one checkpoint is at risk")
	storeEvery := fs.Duration("store-every", 5*time.Second, "how often the storage conditions are read")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// A root and an explicit list are the same thing after this block; the root keeps being read
	// every tick, so a directory that appears joins without a restart.
	var ms []unit.Member
	var source func() ([]unit.Member, error)
	if *membersFrom != "" {
		root := *membersFrom
		source = func() ([]unit.Member, error) { return discoverMembers(root) }
		found, err := source()
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return 2
		}
		ms = found
	} else {
		for _, d := range splitMembers(*members) {
			ms = append(ms, unit.Member{ID: d, Name: filepath.Base(d), Dir: d})
		}
	}
	if len(ms) == 0 {
		fmt.Fprint(out, "usage: ckptctl supervise --members=<dir1,dir2,...> | --members-from=<root>\n")
		return 2
	}

	cfg, _, err := adapters.LoadConfig(ms[0].Dir)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 2
	}
	cfg.FreeSpaceFactor, cfg.StoreEvery = *freeFactor, *storeEvery

	host, _ := os.Hostname()
	sup := supervisor.Open(supervisor.Config{
		Policy: policy.Risk{},
		Bounds: policy.Bounds{
			MinCopies: 1,
			NoUs:      policy.NoUsNone,
		},
		Probes: adapters.All(cfg),
		// One holder for the whole supervisor: the lease is per directory, and every unit has its own.
		Holder: fmt.Sprintf("%s-%d", host, os.Getpid()),
	})
	defer sup.Close()

	if err := sup.Sync(ms); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}

	if *forever {
		done := superviseStop
		if done == nil {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			done = ctx.Done()
		}
		return guard(out, sup, *every, done, source)
	}

	for i := 0; i < *rounds; i++ {
		refresh(out, sup, source)
		if _, err := sup.Tick(); err != nil {
			fmt.Fprintf(out, "error: tick: %v\n", err)
			return 1
		}
		time.Sleep(*every)
	}
	rollup(out, sup)
	return 0
}

// superviseStop is nil in production. A test sets it to a closed channel so the loop can be stopped
// without sending a signal to the test binary; production never reads it.
var superviseStop <-chan struct{}

// guard is the loop a manifest starts: tick until the caller says stop, report a failed tick without
// giving up on the other units, and print the rollup once on the way out.
func guard(out io.Writer, sup *supervisor.Supervisor, every time.Duration, done <-chan struct{},
	source func() ([]unit.Member, error)) int {
	ticks := 0
	for {
		refresh(out, sup, source)
		if _, err := sup.Tick(); err != nil {
			fmt.Fprintf(out, "error: tick %d: %v\n", ticks+1, err)
		}
		ticks++
		select {
		case <-done:
			fmt.Fprintf(out, "supervisor stopped · units=%d ticks=%d\n", len(sup.Dirs()), ticks)
			rollup(out, sup)
			return 0
		case <-time.After(every):
		}
	}
}

// refresh re-reads the members when a root was given. A directory that appeared joins the
// supervisor and one that disappeared is closed by Sync; the line is printed only when the set
// changed, so a steady root prints nothing.
func refresh(out io.Writer, sup *supervisor.Supervisor, source func() ([]unit.Member, error)) {
	if source == nil {
		return
	}
	ms, err := source()
	if err != nil {
		fmt.Fprintf(out, "error: members: %v\n", err)
		return
	}
	before := map[string]bool{}
	for _, d := range sup.Dirs() {
		before[d] = true
	}
	if err := sup.Sync(ms); err != nil {
		fmt.Fprintf(out, "error: members: %v\n", err)
		return
	}
	after := map[string]bool{}
	added, removed := 0, 0
	for _, d := range sup.Dirs() {
		after[d] = true
		if !before[d] {
			added++
		}
	}
	for d := range before {
		if !after[d] {
			removed++
		}
	}
	if added+removed > 0 {
		fmt.Fprintf(out, "members: added=%d removed=%d units=%d\n", added, removed, len(after))
	}
}

// discoverMembers is --members-from: the immediate children of a root that are checkpoint
// directories. A child counts when it holds `.ckpt-governor`, the marker the controller writes; a
// plain subdirectory of a shared volume is not a unit.
func discoverMembers(root string) ([]unit.Member, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]unit.Member, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, advise.BaseDir)); err != nil {
			continue
		}
		out = append(out, unit.Member{ID: dir, Name: e.Name(), Dir: dir})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

// rollup is one row per unit, then one line that adds them up. It reads what each loop published
// rather than re-deciding anything.
func rollup(out io.Writer, sup *supervisor.Supervisor) {
	save, skip, sealed, decided := 0, 0, 0, 0
	dirs := append([]string(nil), sup.Dirs()...)
	sort.Strings(dirs)
	for _, d := range dirs {
		action := "-"
		if dec, ok := advise.LastDecision(d); ok {
			decided++
			action = string(dec.Action)
			if dec.Action == policy.ActionSave {
				save++
			} else {
				skip++
			}
		}
		if n := sealedCopies(d); n > 0 {
			sealed++
		}
		fmt.Fprintf(out, "%-40s %-8s %s\n", d, action, sup.Queue().String())
	}
	fmt.Fprintf(out, "\nsupervise: units=%d sealed=%d decided=%d save=%d skip=%d\n",
		len(dirs), sealed, decided, save, skip)
}

// splitMembers accepts "a,b,c" and ignores empty entries, which is what a shell leaves behind after
// a trailing comma.
func splitMembers(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sealedCopies counts the directories a writer sealed. A unit with one is a unit whose work is on
// disk, which is the only thing the rollup claims about it.
func sealedCopies(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "step-") {
			n++
		}
	}
	return n
}

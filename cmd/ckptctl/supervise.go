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
	"github.com/bolubo/checkpoint-governor/internal/conditions"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce"
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
	membersFrom := fs.String("members-from", "", "roots whose immediate children are checkpoint directories; "+
		"comma-separated, and `cluster=root` names the world each root reads")
	rounds := fs.Int("rounds", 6, "how many ticks to run")
	forever := fs.Bool("forever", false, "run until SIGTERM or SIGINT instead of --rounds")
	every := fs.Duration("every", 200*time.Millisecond, "between ticks")
	freeFactor := fs.Float64("free-factor", 2, "free space below this multiple of one checkpoint is at risk")
	storeEvery := fs.Duration("store-every", 5*time.Second, "how often the storage conditions are read")
	webhook := fs.String("webhook", "", "POST one JSON event to this URL when a plan changes")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// A root and an explicit list are the same thing after this block; the root keeps being read
	// every tick, so a directory that appears joins without a restart.
	var ms []unit.Member
	var source func() ([]unit.Member, error)
	if *membersFrom != "" {
		roots := parseRoots(*membersFrom)
		source = func() ([]unit.Member, error) { return discoverAll(roots) }
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
		fmt.Fprint(out, "usage: ckptctl supervise --members=<dir1,dir2,...> | --members-from=<cluster=root,...>\n")
		return 2
	}

	// The first member set is validated before anything is governed: at startup a malformed
	// conditions.d is an operator mistake we can refuse, and a refusal is clearer than a run that
	// silently watches fewer conditions than the operator wrote. A unit that appears later is a
	// different case: the built-ins still protect it, and setFor says what was wrong.
	for _, m := range ms {
		if _, err := conditions.Load(m.Dir); err != nil {
			fmt.Fprintf(out, "error: %s: %v\n", m.Dir, err)
			return 2
		}
	}

	host, _ := os.Hostname()

	// One probe set per (worldDir, cluster). The cache is what makes a two-cluster supervisor
	// one process rather than one process per cluster: the second unit of a cluster reuses the set
	// the first one built. The deployment's own conditions (conditions.d) are per directory, so they
	// are loaded and cached separately and appended: a job's added condition is that job's, not its
	// cluster's.
	built := map[string][]produce.Probe{}
	added := map[string]conditions.Set{}
	setFor := func(dir string) conditions.Set {
		if s, ok := added[dir]; ok {
			return s
		}
		s, err := conditions.Load(dir)
		if err != nil {
			// A unit that appears later with a malformed conditions.d must not stop the units that
			// are already governed: the built-ins still protect it, and the operator is told.
			fmt.Fprintf(out, "error: %s: %v\n", dir, err)
			s = conditions.Set{}
		}
		added[dir] = s
		return s
	}
	probesFor := func(u unit.Unit) []produce.Probe {
		key := u.WorldDir + "|" + u.Cluster
		p, ok := built[key]
		if !ok {
			cfg, _, err := adapters.LoadConfig(u.Dir)
			if err != nil {
				fmt.Fprintf(out, "error: %s: %v\n", u.Dir, err)
				cfg = adapters.Config{}
			}
			cfg.FreeSpaceFactor, cfg.StoreEvery = *freeFactor, *storeEvery
			if u.WorldDir != "" {
				cfg.WorldDir = u.WorldDir
			}
			if u.Cluster != "" {
				cfg.Cluster = u.Cluster
			}
			cfg = cfg.WithWorld(u.Dir)
			p = adapters.All(cfg)
			built[key] = p
		}
		return append(append([]produce.Probe{}, p...), setFor(u.Dir).Probes...)
	}
	policyFor := func(u unit.Unit) policy.Policy {
		return policy.Risk{Extra: setFor(u.Dir).Rows}
	}

	sup := supervisor.Open(supervisor.Config{
		Policy: policy.Risk{},
		Bounds: policy.Bounds{
			MinCopies: 1,
			NoUs:      policy.NoUsNone,
		},
		ProbesFor: probesFor,
		PolicyFor: policyFor,
		// One holder for the whole supervisor: the lease is per directory, and every unit has its own.
		Holder: fmt.Sprintf("%s-%d", host, os.Getpid()),
		Notify: webhookPoster(*webhook),
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
	after := sup.Units()
	seen := map[string]bool{}
	added, removed := 0, 0
	clusters := map[string]bool{}
	for _, u := range after {
		seen[u.Dir] = true
		if !before[u.Dir] {
			added++
		}
		if u.Cluster != "" {
			clusters[u.Cluster] = true
		}
	}
	for d := range before {
		if !seen[d] {
			removed++
		}
	}
	if added+removed > 0 {
		fmt.Fprintf(out, "members: added=%d removed=%d units=%d clusters=%d\n",
			added, removed, len(after), len(clusters))
	}
}

// root is one `--members-from` entry: a directory whose immediate children are checkpoint
// directories, and the cluster whose world files those units read.
type membersRoot struct {
	cluster string
	dir     string
}

// parseRoots accepts "kind-a=/ckpt-a,kind-b=/ckpt-b", and "/ckpt" for the single-cluster form: no
// cluster name, and each job keeps its world beside itself, which is what WithWorld resolves.
func parseRoots(s string) []membersRoot {
	var out []membersRoot
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		r := membersRoot{dir: p}
		if i := strings.Index(p, "="); i > 0 {
			r.cluster = strings.TrimSpace(p[:i])
			r.dir = strings.TrimSpace(p[i+1:])
		}
		out = append(out, r)
	}
	return out
}

// discoverAll reads every root in one call, so a unit that appears under either root joins the same
// supervisor and is counted once in the rollup.
func discoverAll(roots []membersRoot) ([]unit.Member, error) {
	var out []unit.Member
	for _, r := range roots {
		ms, err := discoverMembers(r)
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

// discoverMembers is --members-from: the immediate children of a root that are checkpoint
// directories. A child counts when it holds `.ckpt-governor`, the marker the controller writes; a
// plain subdirectory of a shared volume is not a unit. A named cluster also sets the world root to
// `<root>/world`, so two clusters can be read by one process.
func discoverMembers(r membersRoot) ([]unit.Member, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	out := make([]unit.Member, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(r.dir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, advise.BaseDir)); err != nil {
			continue
		}
		m := unit.Member{ID: dir, Name: e.Name(), Dir: dir, Cluster: r.cluster}
		if r.cluster != "" {
			m.WorldDir = filepath.Join(r.dir, "world")
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

// rollup is one row per unit, then one line that adds them up. It reads what each loop published
// rather than re-deciding anything. The cluster count is there because a two-cluster supervisor is
// one process, and "units=2 clusters=2" is what proves the discovery crossed the cluster boundary.
func rollup(out io.Writer, sup *supervisor.Supervisor) {
	units := sup.Units()
	save, skip, sealed, decided := 0, 0, 0, 0
	clusters := map[string]bool{}
	for _, u := range units {
		if u.Cluster != "" {
			clusters[u.Cluster] = true
		}
		d := u.Dir
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
	fmt.Fprintf(out, "\nsupervise: units=%d clusters=%d sealed=%d decided=%d save=%d skip=%d\n",
		len(units), len(clusters), sealed, decided, save, skip)
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

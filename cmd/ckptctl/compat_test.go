package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/metrics"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// The names other people build on are frozen here: a condition a dashboard filters on, an event kind
// a log shipper keys on, a metric a graph is drawn from. Renaming one is a compatibility change, so it
// has to fail this test first — that is the whole reason the list is written out by hand.
//
// Adding is allowed: a new condition, kind or metric appears in code and here in the same commit.
func TestFrozenNames(t *testing.T) {
	// 0013's catalogue, in precedence order.
	wantConditions := []string{
		"watch.broken", "risk.preempt", "risk.shutdown", "store.low-space",
		"host.memory-pressure", "host.gpu-fault", "job.ending", "risk.eviction-likely",
		"store.blocked", "store.slow", "host.gpu-hot", "host.cpu-throttled",
		"job.step-slow", "net.long-outage",
	}
	rows := policy.Conditions()
	if len(rows) != len(wantConditions) {
		t.Fatalf("%d conditions in the code, %d in the frozen list: %v",
			len(rows), len(wantConditions), namesOfConditions(rows))
	}
	for i, want := range wantConditions {
		if rows[i].Name != want {
			t.Fatalf("condition %d = %s, want %s (order is precedence)", i, rows[i].Name, want)
		}
	}

	wantKinds := []string{
		"decision", "publish", "attempt_start", "attempt_end", "condition",
		"lease", "backoff", "error", "config",
	}
	kinds := advise.Kinds()
	if len(kinds) != len(wantKinds) {
		t.Fatalf("%d event kinds in the code, %d in the frozen list", len(kinds), len(wantKinds))
	}
	for i, want := range wantKinds {
		if string(kinds[i]) != want {
			t.Fatalf("event kind %d = %q, want %q", i, kinds[i], want)
		}
	}

	wantMetrics := []string{
		"checkpoint_staleness_seconds", "checkpoint_over_bound_seconds",
		"checkpoint_blind_conditions", "checkpoint_writes_total",
		"checkpoint_units", "checkpoint_clusters",
	}
	gotMetrics := metrics.Names()
	if len(gotMetrics) != len(wantMetrics) {
		t.Fatalf("%d metrics in the code, %d in the frozen list", len(gotMetrics), len(wantMetrics))
	}
	for i, want := range wantMetrics {
		if gotMetrics[i] != want {
			t.Fatalf("metric %d = %q, want %q", i, gotMetrics[i], want)
		}
	}

	// Exit codes are behaviour, not constants: a script keys on them, so the two that are decisions
	// about whether the binary answered are asserted here. 0 = answered, 2 = not asked properly.
	if code := runDoctor(nil, io.Discard); code != 2 {
		t.Fatalf("a missing --dir exited %d, want 2", code)
	}
	empty := t.TempDir()
	if code := runDoctor([]string{"--dir", empty}, io.Discard); code != 0 {
		t.Fatalf("a readable directory exited %d, want 0", code)
	}

	t.Logf("frozen: %d conditions, %d event kinds, %d metrics, 2 exit codes",
		len(rows), len(kinds), len(gotMetrics))
}

func namesOfConditions(rows []policy.Condition) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// A run on a directory written by the previous build continues its history instead of starting over:
// the ledger is read (so the epoch advances from it, not from zero), nothing is refused as a foreign
// schema, and the unit's generation is untouched. The epoch itself is per-process by design — a new
// controller is a new epoch — so "kept" would be the wrong assertion; "continued" is the right one.
func TestARunOnAnOldDirectoryContinuesItsHistory(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, filepath.Join("..", "..", "internal", "advise", "testdata", "schema-2", "job-1"), dir)

	before, ok := advise.ReadPublished(dir)
	if !ok {
		t.Fatal("the fixture's plan was refused before the run")
	}
	if problem := advise.SchemaProblem(dir); problem != "" {
		t.Fatalf("the fixture was already a foreign schema: %s", problem)
	}

	if code := runRun([]string{"--dir=" + dir, "--unit=job-1", "--once", "--no-us=none"}, io.Discard); code != 0 {
		t.Fatalf("run --once exited %d on an old directory", code)
	}
	if problem := advise.SchemaProblem(dir); problem != "" {
		t.Fatalf("the run left a plan this binary cannot read: %s", problem)
	}
	after, ok := advise.ReadPublished(dir)
	if !ok {
		t.Fatal("the run left no readable plan")
	}
	// A fresh directory starts at epoch 1; this one came from a ledger that already had one, so it
	// must be above that baseline. A reset to 1 would mean the old state was thrown away.
	if after.Epoch <= before.Epoch {
		t.Fatalf("epoch %d did not advance past the fixture's %d: the old ledger was not continued",
			after.Epoch, before.Epoch)
	}
	if after.Decision.Generation != before.Decision.Generation {
		t.Fatalf("generation changed from %d to %d", before.Decision.Generation, after.Decision.Generation)
	}
	if after.Decision.UnitID != "job-1" {
		t.Fatalf("unit id = %q, want the one the fixture recorded", after.Decision.UnitID)
	}
}

// copyFixture copies a directory tree, so a test can write into it without touching the fixture.
func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
	if err != nil {
		t.Fatalf("copying the fixture: %v", err)
	}
	if strings.Contains(dst, "schema-2") {
		t.Fatalf("a test wrote into the fixture itself: %s", dst)
	}
}

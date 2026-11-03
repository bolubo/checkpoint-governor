package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// The announcement's acceptance: a deletion timestamp is an announcement, and an announcement is a save — without
// any pressure in the world, because the platform has already decided.
func TestAnAnnouncementDecidesOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	writeAnnouncedWorld(t, dir, true, 10)

	if code := runRun([]string{"--dir", dir, "--once", "--no-us=none"}, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}

	d, ok := advise.LastDecision(dir)
	if !ok {
		t.Fatal("no plan was published")
	}
	if d.Action != policy.ActionSave || d.Reason != policy.ReasonRisk {
		t.Fatalf("decision = %s/%s, want save/risk", d.Action, d.Reason)
	}
	if len(d.Reasons) == 0 || d.Reasons[0].Code != "risk.preempt" {
		t.Fatalf("the decision does not name the announcement: %+v", d.Reasons)
	}
	fmt.Println("preempt 1 after deletionTimestamp → save")
}

// The gate's record reaches both ends of the pipeline: the loop decides on it, and `explain` names it.
// The world has no pressure and no deletion timestamp, so this is the whole case for decision 0030.
func TestAHeldReclaimReachesTheDecisionAndTheExplanation(t *testing.T) {
	dir := t.TempDir()
	writeAnnouncedWorld(t, dir, false, 10)
	cfg := adapters.Config{}.WithWorld(dir)
	now := time.Now().UTC()
	if err := adapters.WriteReclaim(cfg.WorldDir, "a", adapters.ReclaimRecord{
		Cluster: "a", UnitID: dir, RequestID: "r-1", Intruder: "intruder",
		State: adapters.ReclaimHeld, Since: now, Deadline: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if code := runRun([]string{"--dir", dir, "--once", "--no-us=none"}, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	d, ok := advise.LastDecision(dir)
	if !ok {
		t.Fatal("no plan was published")
	}
	if d.Action != policy.ActionSave || len(d.Reasons) == 0 || d.Reasons[0].Code != "risk.preempt" {
		t.Fatalf("decision = %s/%s %+v, want save/risk naming risk.preempt", d.Action, d.Reason, d.Reasons)
	}

	var out strings.Builder
	if code := runExplain([]string{"--dir", dir}, &out); code != 0 {
		t.Fatalf("explain exited %d", code)
	}
	text := out.String()
	for _, want := range []string{"basis      announcement: the reclaim gate is holding intruder", "(reclaim r-1)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("explain is missing %q:\n%s", want, text)
		}
	}
	fmt.Println("reclaim held → save/risk → explain names the hold")
}

// A reclaim is a new event even when the waiting work is unchanged: the gate is asking for a seal
// now, and the old event id would dedupe that write as already paid (0030).
func TestAHeldReclaimIsANewEvent(t *testing.T) {
	dir := t.TempDir()
	writeAnnouncedWorld(t, dir, false, 10)
	cfg := adapters.Config{}.WithWorld(dir)
	now := time.Now().UTC()

	if got := causeFor(cfg)(now); got != "" {
		t.Fatalf("a quiet world named event %q", got)
	}
	if err := adapters.WriteReclaim(cfg.WorldDir, "a", adapters.ReclaimRecord{
		Cluster: "a", UnitID: cfg.UnitID, RequestID: "r-1", Intruder: "intruder",
		State: adapters.ReclaimHeld, Since: now, Deadline: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if got := causeFor(cfg)(now); got != "reclaim:r-1" {
		t.Fatalf("event = %q, want reclaim:r-1", got)
	}
}

// The world's unit id is what the run was told with --unit, not the directory's basename. An
// observer publishes `namespace/pod`; hashing the basename made the probes look up a unit nobody
// wrote, so a held reclaim was reported as blindness exactly when it mattered (0030).
func TestTheProbesUseTheUnitFlagNotTheDirectoryName(t *testing.T) {
	dir := t.TempDir()
	cfg := adapters.Config{}.WithWorld(dir)
	unit := "default/victim"
	now := time.Now().UTC()
	if err := adapters.WriteSummary(cfg.WorldDir, adapters.WorldSummary{
		Schema: 1, Cluster: "a", At: now, StaleAfterSeconds: 30,
		Capabilities: map[string]adapters.CapabilityWire{
			"preemption": {Exists: true, Source: "test"},
			"eviction":   {Exists: true, Source: "test"},
		},
		Nodes: []adapters.NodeState{{
			Name:        "worker-1",
			Allocatable: adapters.Resources{CPUMilli: 24000, MemBytes: 16 << 30},
			Requested:   adapters.Resources{CPUMilli: 2400},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := adapters.WriteUnit(cfg.WorldDir, "a", adapters.UnitRecord{
		Cluster: "a", UnitID: unit, At: now, Node: "worker-1", Priority: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := adapters.WriteReclaim(cfg.WorldDir, "a", adapters.ReclaimRecord{
		Cluster: "a", UnitID: unit, RequestID: "r-1", Intruder: "intruder",
		State: adapters.ReclaimHeld, Since: now, Deadline: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	if code := runRun([]string{"--dir", dir, "--unit=" + unit, "--once", "--no-us=none"}, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	d, ok := advise.LastDecision(dir)
	if !ok {
		t.Fatal("no plan was published")
	}
	if d.Action != policy.ActionSave || len(d.Reasons) == 0 || d.Reasons[0].Code != "risk.preempt" {
		t.Fatalf("decision = %s/%s %+v, want save/risk naming risk.preempt", d.Action, d.Reason, d.Reasons)
	}
	for _, m := range d.Missing {
		if strings.HasPrefix(m, "blind:") {
			t.Fatalf("the probe was blind to a unit that exists: %v", d.Missing)
		}
	}

	var out strings.Builder
	if code := runExplain([]string{"--dir", dir}, &out); code != 0 {
		t.Fatalf("explain exited %d", code)
	}
	if strings.Contains(out.String(), "this unit is not placed") {
		t.Fatalf("explain looked up the directory name instead of the plan's unit:\n%s", out.String())
	}
}

// The same world without the timestamp is quiet: nothing is at risk, so nothing is written.
func TestAWorldWithoutAnAnnouncementStaysQuiet(t *testing.T) {
	dir := t.TempDir()
	writeAnnouncedWorld(t, dir, false, 10)

	if code := runRun([]string{"--dir", dir, "--once", "--no-us=none"}, io.Discard); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	d, _ := advise.LastDecision(dir)
	if d.Action == policy.ActionSave {
		t.Fatalf("a calm world asked for a write: %+v", d)
	}
}

// writeAnnouncedWorld builds the smallest complete world: one summary, one unit record, and the
// platform's decision (or not) on that unit.
func writeAnnouncedWorld(t *testing.T, dir string, announced bool, cpuPct int) {
	t.Helper()
	cfg := adapters.Config{}.WithWorld(dir)
	now := time.Now().UTC()
	if err := adapters.WriteSummary(cfg.WorldDir, adapters.WorldSummary{
		Schema: 1, Cluster: "a", At: now, StaleAfterSeconds: 30,
		Capabilities: map[string]adapters.CapabilityWire{
			"preemption": {Exists: true, Source: "test"},
			"eviction":   {Exists: true, Source: "test"},
		},
		Nodes: []adapters.NodeState{{
			Name:        "worker-1",
			Allocatable: adapters.Resources{CPUMilli: 24000, MemBytes: 16 << 30},
			Requested:   adapters.Resources{CPUMilli: int64(24000 * cpuPct / 100)},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	u := adapters.UnitRecord{Cluster: "a", UnitID: dir, At: now, Node: "worker-1", Priority: 1000}
	if announced {
		d := now.Add(-time.Second)
		u.DeletionTimestamp = &d
	}
	if err := adapters.WriteUnit(cfg.WorldDir, "a", u); err != nil {
		t.Fatal(err)
	}
	if cfg.UnitID != filepath.Base(dir) {
		t.Fatalf("unit id = %q, want the directory name %q", cfg.UnitID, filepath.Base(dir))
	}
}

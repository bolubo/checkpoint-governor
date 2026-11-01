package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
	"github.com/bolubo/checkpoint-governor/internal/supervisor"
	"github.com/bolubo/checkpoint-governor/internal/unit"
)

// The unit of work is not the process. One supervisor drives N directories, and the rollup is
// one line — the person watching three jobs should not need three terminals.
func TestSuperviseRollsUpEveryUnit(t *testing.T) {
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{a, b, c} {
		write(t, dir, "ending", "1")
	}

	var out strings.Builder
	code := runSupervise([]string{
		"--members", strings.Join([]string{a, b, c}, ","),
		"--rounds", "2", "--every", "10ms",
	}, &out)
	if code != 0 {
		t.Fatalf("exit = %d:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "supervise: units=3") {
		t.Fatalf("the rollup does not count the units:\n%s", text)
	}
	// Every unit was told to stop by its own `ending`, so every unit must have a plan saying save.
	if !strings.Contains(text, "decided=3 save=3 skip=0") {
		t.Fatalf("the rollup does not add up:\n%s", text)
	}
	t.Logf("%s", strings.TrimSpace(text[strings.LastIndex(text, "supervise:"):]))
}

// One directory is a legal deployment, and it is also the case where a bug in the rollup hides.
func TestSuperviseAcceptsASingleUnit(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ending", "1")

	var out strings.Builder
	if code := runSupervise([]string{"--members", dir, "--rounds", "1", "--every", "1ms"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "supervise: units=1") {
		t.Fatalf("output = %s", out.String())
	}
}

// A member list with a trailing comma is what a shell leaves behind; it must not create an empty
// unit that reads as a directory called "".
func TestSplitMembersIgnoresEmptyEntries(t *testing.T) {
	got := splitMembers(" a , b ,, c ,")
	if strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("split = %v", got)
	}
	_ = time.Millisecond
}

// --forever is the shape a manifest starts: tick until told to stop, then print the same rollup
// --rounds would have printed. The stop is injected through superviseStop, so the test does not have
// to send a signal to its own process.
func TestSuperviseRunsUntilAskedToStop(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, dir := range []string{a, b} {
		write(t, dir, "ending", "1")
	}

	done := make(chan struct{})
	close(done)
	old := superviseStop
	superviseStop = done
	defer func() { superviseStop = old }()

	var out strings.Builder
	code := runSupervise([]string{
		"--members", strings.Join([]string{a, b}, ","),
		"--forever", "--every", "1ms",
	}, &out)
	if code != 0 {
		t.Fatalf("exit = %d:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "supervisor stopped · units=2 ticks=1") {
		t.Fatalf("the stop line is missing:\n%s", text)
	}
	if !strings.Contains(text, "supervise: units=2") {
		t.Fatalf("the rollup is missing after the stop:\n%s", text)
	}
	t.Logf("%s", strings.TrimSpace(out.String()))
}

// --members-from: the immediate children of a root that hold `.ckpt-governor` are units, and a
// plain subdirectory of a shared volume is not.
func TestMembersFromDiscoversOnlyGovernedDirectories(t *testing.T) {
	root := t.TempDir()
	unitDir := filepath.Join(root, "job-a")
	plain := filepath.Join(root, "not-a-unit")
	hidden := filepath.Join(root, ".trash")
	for _, d := range []string{unitDir, plain, hidden} {
		if err := os.MkdirAll(filepath.Join(d, ".ckpt-governor"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(plain, ".ckpt-governor")); err != nil {
		t.Fatal(err)
	}

	ms, err := discoverAll([]membersRoot{{dir: root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Name != "job-a" {
		t.Fatalf("discovered %+v, want only job-a", ms)
	}
}

// A root that gains and loses a job is the case the refresh line exists for: the supervisor must
// follow the directory, not the process start.
func TestRefreshFollowsTheRoot(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	if err := os.MkdirAll(filepath.Join(a, ".ckpt-governor"), 0o755); err != nil {
		t.Fatal(err)
	}
	sup := supervisor.Open(supervisor.Config{
		Policy: policy.Risk{},
		Bounds: policy.Bounds{MinCopies: 1, NoUs: policy.NoUsNone},
		Holder: "test",
	})
	defer sup.Close()
	ms, err := discoverAll([]membersRoot{{dir: root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Sync(ms); err != nil {
		t.Fatal(err)
	}
	source := func() ([]unit.Member, error) { return discoverAll([]membersRoot{{dir: root}}) }

	var out strings.Builder
	b := filepath.Join(root, "b")
	if err := os.MkdirAll(filepath.Join(b, ".ckpt-governor"), 0o755); err != nil {
		t.Fatal(err)
	}
	refresh(&out, sup, source)
	if !strings.Contains(out.String(), "members: added=1 removed=0 units=2 clusters=0") {
		t.Fatalf("a new job was not reported:\n%s", out.String())
	}

	out.Reset()
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	refresh(&out, sup, source)
	if !strings.Contains(out.String(), "members: added=0 removed=1 units=1 clusters=0") {
		t.Fatalf("a removed job was not reported:\n%s", out.String())
	}
	t.Logf("%s", strings.TrimSpace(out.String()))
}

// Two clusters in one process: each unit reads its own world. This is the test that a single shared
// probe set cannot pass — with one set, both units would see kind-a's announcement.
func TestTwoClustersReadTheirOwnWorld(t *testing.T) {
	cases := []struct {
		cluster   string
		announced bool
	}{{"kind-a", true}, {"kind-b", false}}

	var members, dirs []string
	for _, c := range cases {
		root := t.TempDir()
		job := filepath.Join(root, "job-"+c.cluster)
		dirs = append(dirs, job)
		if err := os.MkdirAll(filepath.Join(job, ".ckpt-governor"), 0o755); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		world := filepath.Join(root, "world")
		if err := adapters.WriteSummary(world, adapters.WorldSummary{
			Cluster: c.cluster, At: now, StaleAfterSeconds: 30,
			// A node with a real request/allocatable ratio: an empty summary makes the inference
			// blind, and blindness is urgent, which is what would make both clusters write.
			Nodes: []adapters.NodeState{{
				Name:        "worker-1",
				Allocatable: adapters.Resources{CPUMilli: 8000, MemBytes: 32 << 30, GPUCount: 1},
				Requested:   adapters.Resources{CPUMilli: 800, MemBytes: 4 << 30},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		rec := adapters.UnitRecord{UnitID: "job-" + c.cluster, At: now, Node: "worker-1"}
		if c.announced {
			stamp := now
			rec.DeletionTimestamp = &stamp
		}
		if err := adapters.WriteUnit(world, c.cluster, rec); err != nil {
			t.Fatal(err)
		}
		members = append(members, c.cluster+"="+root)
	}

	var out strings.Builder
	if code := runSupervise([]string{
		"--members-from", strings.Join(members, ","),
		"--rounds", "1", "--every", "1ms",
	}, &out); code != 0 {
		t.Fatalf("exit = %d:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "clusters=2") {
		t.Fatalf("the rollup does not count both clusters:\n%s", text)
	}
	if !strings.Contains(text, "save=1 skip=1") {
		var why strings.Builder
		for _, d := range dirs {
			if dec, ok := advise.LastDecision(d); ok {
				fmt.Fprintf(&why, "%s: %s %v\n", d, dec.Reason, dec.Reasons)
			}
		}
		t.Fatalf("the announced cluster did not write, or the calm one did:\n%s\n%s", text, why.String())
	}
	t.Logf("%s", strings.TrimSpace(text))
}

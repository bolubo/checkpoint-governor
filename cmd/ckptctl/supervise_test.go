package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
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

	ms, err := discoverMembers(root)
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
	ms, err := discoverMembers(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Sync(ms); err != nil {
		t.Fatal(err)
	}
	source := func() ([]unit.Member, error) { return discoverMembers(root) }

	var out strings.Builder
	b := filepath.Join(root, "b")
	if err := os.MkdirAll(filepath.Join(b, ".ckpt-governor"), 0o755); err != nil {
	t.Fatal(err)
	}
	refresh(&out, sup, source)
	if !strings.Contains(out.String(), "members: added=1 removed=0 units=2") {
		t.Fatalf("a new job was not reported:\n%s", out.String())
	}

	out.Reset()
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	refresh(&out, sup, source)
	if !strings.Contains(out.String(), "members: added=0 removed=1 units=1") {
		t.Fatalf("a removed job was not reported:\n%s", out.String())
	}
	t.Logf("%s", strings.TrimSpace(out.String()))
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/advise"
)

// With nothing reported and no window, the answer is "do not write": that is the whole
// product, and the plan is still published so the job knows the controller is alive and calm.
func TestRunOnceIsCalmWithNothingToReport(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if code := runRun([]string{"--dir=" + dir, "--once", "--no-us=none"}, &out); code != 0 {
		t.Fatalf("run --once exited %d", code)
	}
	if !strings.Contains(out.String(), "action=skip") {
		t.Fatalf("a job with no conditions must be skipped:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, advise.BaseDir, "state.json")); err != nil {
		t.Fatalf("the plan was not published: %v", err)
	}
}

// A write in flight is a job-local fact, and safety beats every condition: the answer is hold.
func TestRunOnceHoldsWhileAWriteIsRunning(t *testing.T) {
	dir := t.TempDir()
	unsealed := filepath.Join(dir, "step-8")
	if err := os.MkdirAll(unsealed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsealed, "shard-0"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if code := runRun([]string{"--dir=" + dir, "--once", "--no-us=none"}, &out); code != 0 {
		t.Fatalf("run --once exited %d", code)
	}
	if !strings.Contains(out.String(), "action=hold") {
		t.Fatalf("an unsealed directory must hold:\n%s", out.String())
	}
}

// No configuration is a configuration (product-v1 §2). A run with no flags starts, treats
// `no-us` as `none`, and says so on its first line: the user is entitled to know which sentence is in
// force, and "unset" is the difference between a policy they chose and one we assumed.
func TestRunStartsWithNoConfiguration(t *testing.T) {
	var out strings.Builder
	if code := runRun([]string{"--dir", t.TempDir(), "--once"}, &out); code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out.String())
	}
	for _, want := range []string{"no-us none (unset — no writes if the governor stops)", "window "} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the start-up line is missing %q:\n%s", want, out.String())
		}
	}
}

// An answer that is neither `none` nor a duration is refused: guessing what someone meant is how a
// silent default becomes a lost day.
func TestRunRejectsAnUnparseableNoUs(t *testing.T) {
	var out strings.Builder
	if code := runRun([]string{"--dir", t.TempDir(), "--no-us=maybe"}, &out); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "either none, or a duration") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRunRefusesWithoutADirectory(t *testing.T) {
	var out strings.Builder
	if code := runRun([]string{"--once"}, &out); code == 0 {
		t.Fatal("a run without --dir must not succeed")
	}
}

// A threshold the code cannot mean must stop the controller before it writes a plan built on it.
func TestRunRefusesAnImpossibleThreshold(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "conditions.json"), []byte(`{"memoryLine": 1.5}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if code := runRun([]string{"--dir", dir, "--once", "--no-us=none"}, &out); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "memoryLine") {
		t.Fatalf("the error does not name the field:\n%s", out.String())
	}
}

// Changing a threshold is a decision, and a decision that leaves no trace is one nobody can audit.
func TestAnOverrideIsRecorded(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "conditions.json"), []byte(`{"freeSpaceFactor": 3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if code := runRun([]string{"--dir", dir, "--once", "--no-us=none"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	events, err := advise.ReadEvents(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind != advise.EventConfig || e.Code != "threshold-override" || e.Values["key"] != "freeSpaceFactor" {
			continue
		}
		// The event must say what it replaced, or it is only half a record: "0.9" is not a change,
		// "0.95 → 0.9" is.
		if e.Values["from"] == nil || e.Values["to"] == nil {
			t.Fatalf("the override event carries no transition: %+v", e.Values)
		}
		if e.Values["to"] != "3" {
			t.Fatalf("to = %v, want the file's 3", e.Values["to"])
		}
		return
	}
	t.Fatalf("no override event was recorded: %+v", events)
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/conditions"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

const customCondition = `{
  "name": "job.loss-exploded",
  "tier": "urgent",
  "missing": "urgent",
  "why": "the loss left its normal range",
  "read": {"file": "team-signal", "on": "1", "off": "0"}
}`

// The whole point of conditions.d: a condition this repository never heard of reaches the decision,
// and the start-up line says how many are watched and where they come from.
func TestAConditionFromDiskReachesTheDecision(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, advise.BaseDir, conditions.DirName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "team.json"), []byte(customCondition), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team-signal"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := runRun([]string{"--dir=" + dir, "--once", "--no-us=none"}, &out); code != 0 {
		t.Fatalf("run --once exited %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "conditions: 14 built-in + 1 from disk") {
		t.Fatalf("start-up does not count the added condition:\n%s", out.String())
	}

	d, ok := advise.LastDecision(dir)
	if !ok {
		t.Fatal("no plan published")
	}
	if d.Action != policy.ActionSave || d.Reason != policy.ReasonRisk {
		t.Fatalf("decision = %s/%s, want save/risk", d.Action, d.Reason)
	}
	if d.Why != "job.loss-exploded: the loss left its normal range" {
		t.Fatalf("the decision does not name the added condition: %q", d.Why)
	}

	// The job writes 0: the same file, no longer at risk, and the answer goes back to calm.
	if err := os.WriteFile(filepath.Join(dir, "team-signal"), []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runRun([]string{"--dir=" + dir, "--once", "--no-us=none"}, &out); code != 0 {
		t.Fatalf("second run exited %d", code)
	}
	if d, ok := advise.LastDecision(dir); !ok || d.Action != policy.ActionSkip {
		t.Fatalf("second decision = %+v, want skip", d)
	}
}

// A file an operator wrote wrong is a startup failure, not a run that quietly watches less than they
// believe.
func TestAMalformedConditionStopsStartup(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, advise.BaseDir, conditions.DirName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(customCondition, `"tier": "urgent"`, `"tier": "loud"`, 1)
	if err := os.WriteFile(filepath.Join(base, "team.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := runRun([]string{"--dir=" + dir, "--once", "--no-us=none"}, &out); code != 2 {
		t.Fatalf("run exited %d, want 2:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "tier") {
		t.Fatalf("the refusal does not name the mistake:\n%s", out.String())
	}
}

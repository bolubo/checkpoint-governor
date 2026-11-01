package stats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/policy"
)

func decision() policy.Decision {
	return policy.Decision{Action: policy.ActionSave, Reason: "risk.preempt", UnitID: "job", CopiesToWrite: 1}
}

// A deployment that has never run is not an error: every count is zero and the summary still prints.
func TestAnEmptyDeploymentIsQuiet(t *testing.T) {
	dir := t.TempDir()
	s, err := Of(dir, time.Now())
	if err != nil {
		t.Fatalf("an empty directory is not an error: %v", err)
	}
	if s.Writes != 0 || s.Failures != 0 || s.PlanKnown {
		t.Fatalf("expected zeroes, got %+v", s)
	}
}

// The numbers come from the ledger, so they survive a restart. Median rather than mean: one slow
// write on a busy mount must not describe the deployment.
func TestTheSummaryComesFromTheLedger(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, advise.BaseDir), 0o755); err != nil {
		t.Fatal(err)
	}

	ledger, err := advise.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, cost := range []float64{3.0, 1.0, 2.0} {
		ledger.AddWriteCost(cost)
	}
	// The ledger keeps the running total itself; the samples are a bounded window of the last few.
	ledger.WriteSecondsTotal = 6.0
	ledger.Failures = 2
	ledger.Reconciled = 1
	if err := ledger.Save(dir); err != nil {
		t.Fatal(err)
	}

	s, err := Of(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Writes != 3 {
		t.Errorf("writes = %d, want 3", s.Writes)
	}
	if s.WriteMedian != 2.0 {
		t.Errorf("median = %v, want 2 (the middle of 1,2,3)", s.WriteMedian)
	}
	if s.WriteLast != 2.0 {
		t.Errorf("last = %v, want the last sample recorded (2)", s.WriteLast)
	}
	if s.WriteTotal != 6.0 {
		t.Errorf("total = %v, want 6", s.WriteTotal)
	}
	if s.Failures != 2 || s.Reconciled != 1 {
		t.Errorf("failures/reconciled = %d/%d, want 2/1", s.Failures, s.Reconciled)
	}
}

// A plan on disk is reported with its age, because "save" three hours ago and "save" three seconds ago
// are different facts about the same word.
func TestThePlanIsReportedWithItsAge(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, advise.BaseDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := advise.Publish(dir, decision(), 1, time.Now(), time.Minute); err != nil {
		t.Fatal(err)
	}
	// The age comes from the file's modification time, so the test moves it rather than pretending:
	// a plan written ninety seconds ago is what a restart finds.
	old := time.Now().Add(-90 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, advise.BaseDir, "state.json"), old, old); err != nil {
		t.Fatal(err)
	}

	s, err := Of(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !s.PlanKnown {
		t.Fatal("the published plan was not found")
	}
	if s.Plan != "save (risk.preempt)" {
		t.Errorf("plan = %q, want the action with the reason that produced it", s.Plan)
	}
	if s.PlanAge < 80*time.Second {
		t.Errorf("age = %v, want roughly 90s", s.PlanAge)
	}
}

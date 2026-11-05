package demo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

func TestCreateCleanIsVerified(t *testing.T) {
	f, err := Create(t.TempDir(), ScenarioClean)
	if err != nil {
		t.Fatal(err)
	}
	v := verify.Classify(f.GenDir, 0, verify.Options{Now: time.Now(), CheckContent: true})
	if v.State != policy.CopyVerified {
		t.Fatalf("clean copy = %s %s", v.State, v.Detail)
	}
}

func TestKillRemovesOneShardAndRepairRestoresIt(t *testing.T) {
	dir := t.TempDir()
	f, err := Create(dir, ScenarioKill)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.GenDir, "rank1.pt")); !os.IsNotExist(err) {
		t.Fatalf("the kill scenario left rank1.pt: %v", err)
	}
	v := verify.Classify(f.GenDir, 0, verify.Options{Now: time.Now(), CheckContent: true})
	if v.State != policy.CopyBroken || v.Reason != verify.ReasonShort {
		t.Fatalf("missing shard = %s %s", v.State, v.Detail)
	}

	if _, err := Repair(dir); err != nil {
		t.Fatal(err)
	}
	v = verify.Classify(f.GenDir, 0, verify.Options{Now: time.Now(), CheckContent: true})
	if v.State != policy.CopyVerified {
		t.Fatalf("repaired copy = %s %s", v.State, v.Detail)
	}
}

func TestTornKeepsSizeAndBreaksTheTail(t *testing.T) {
	f, err := Create(t.TempDir(), ScenarioTorn)
	if err != nil {
		t.Fatal(err)
	}
	v := verify.Classify(f.GenDir, 0, verify.Options{Now: time.Now(), CheckContent: true})
	if v.State != policy.CopyBroken || v.Reason != verify.ReasonHash {
		t.Fatalf("torn copy = %s %s", v.State, v.Detail)
	}
}

// The marker is the only thing that lets a second run touch the directory.
func TestCreateRefusesAForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, ScenarioClean); err == nil {
		t.Fatal("a non-empty foreign directory must be refused")
	}
	if err := Clean(dir); err == nil {
		t.Fatal("clean must refuse a directory without the demo marker")
	}
}

func TestUnknownScenario(t *testing.T) {
	if _, err := ParseScenario("explode"); err == nil {
		t.Fatal("an unknown scenario must be refused")
	}
}

package evidence

import (
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/demo"
)

// The bundle must carry the same verdict the individual commands would: a missing shard is not
// resumable, the newest copy is broken, and exactly one problem names the shard.
func TestCollectAMissingShard(t *testing.T) {
	dir := t.TempDir()
	f, err := demo.Create(dir, demo.ScenarioKill)
	if err != nil {
		t.Fatal(err)
	}
	b := Collect(f.Dir)
	if b.Schema != 1 {
		t.Fatalf("schema = %d", b.Schema)
	}
	if b.Resume.State != "not resumable" {
		t.Fatalf("resume = %+v", b.Resume)
	}
	if b.Newest == nil || b.Newest.Verified {
		t.Fatalf("newest = %+v", b.Newest)
	}
	if len(b.Problems) != 1 || b.Problems[0].Code != "shard_missing" {
		t.Fatalf("problems = %+v", b.Problems)
	}
	if b.Storage == nil || b.Storage.Class == "" {
		t.Fatalf("storage = %+v", b.Storage)
	}
}

func TestCollectACleanCopyHasNoProblems(t *testing.T) {
	dir := t.TempDir()
	f, err := demo.Create(dir, demo.ScenarioClean)
	if err != nil {
		t.Fatal(err)
	}
	b := Collect(f.Dir)
	if b.Resume.State != "resumable" {
		t.Fatalf("resume = %+v", b.Resume)
	}
	if !b.Newest.Verified {
		t.Fatalf("newest = %+v", b.Newest)
	}
	if len(b.Problems) != 0 {
		t.Fatalf("problems = %+v", b.Problems)
	}
}

func TestCollectATornCopyIsATailProblem(t *testing.T) {
	dir := t.TempDir()
	f, err := demo.Create(dir, demo.ScenarioTorn)
	if err != nil {
		t.Fatal(err)
	}
	b := Collect(f.Dir)
	if len(b.Problems) != 1 || b.Problems[0].Code != "tail_mismatch" {
		t.Fatalf("problems = %+v", b.Problems)
	}
	// The shard is present, so the declaration is still resumable; the content check is what fails.
	if b.Resume.State != "resumable" {
		t.Fatalf("resume = %+v", b.Resume)
	}
}

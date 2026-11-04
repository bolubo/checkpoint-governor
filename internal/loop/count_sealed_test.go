package loop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// Retention counts only our own copies: a framework's checkpoint is the framework's to remove, and
// counting it as one of ours would make a good copy look expendable.
func TestRetentionNeverCountsAFrameworkCopy(t *testing.T) {
	copies := []policy.Copy{
		{Step: 10, State: policy.CopySealed},
		{Step: 5, State: policy.CopySealed, Kind: "hf-checkpoint"},
		{Step: 1, State: policy.CopySealed},
	}
	if got := retirableBelow(10, 2, copies); len(got) != 0 {
		t.Fatalf("retirable = %+v, want none: the two newest of ours fill keep=2", got)
	}
	if got := retirableBelow(10, 1, copies); len(got) != 1 || got[0].Step != 1 {
		t.Fatalf("retirable = %+v, want only step 1", got)
	}
}

// Both layouts count, because both are written by real writers (F11).
//
// The receipt says how many checkpoints exist "whoever wrote them", and it said zero while a `gen-` directory
// sat on disk with a sealed manifest — the one sentence this product must never get wrong.
func TestASealedGenerationIsCounted(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, sealed bool) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if sealed {
			if err := os.WriteFile(filepath.Join(path, "manifest.json"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	write("step-1", true)         // the older writer
	write("gen-0002", true)       // the contract writer (0025)
	write("gen-0003", false)      // a write in flight: not sealed, so not counted
	write(".ckpt-governor", true) // not a checkpoint at all, whatever it holds

	if got := countSealed(dir); got != 2 {
		t.Fatalf("countSealed = %d, want 2 (step-1 and gen-0002)", got)
	}
}

// The receipt's number has to be the same number `status` prints: both ask the finder, so a framework
// checkpoint counts as one whoever wrote it.
func TestAFrameworkCheckpointIsCountedWhoeverWroteIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "gen-0002"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gen-0002", "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "checkpoint-1000"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checkpoint-1000", "trainer_state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := countSealed(dir); got != 2 {
		t.Fatalf("countSealed = %d, want 2 (gen-0002 and checkpoint-1000)", got)
	}
	// A half-written framework directory is not a copy.
	if err := os.MkdirAll(filepath.Join(dir, "checkpoint-2000"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := countSealed(dir); got != 2 {
		t.Fatalf("countSealed = %d after a half-written directory, want 2", got)
	}
}

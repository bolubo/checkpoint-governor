package loop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
)

// TestMissingStatesComesFromTheDeclaration is the reader half of "incomplete is a finer kind of
// broken" (0026): the declaration names what it holds, and we name what a full restore would lack.
func TestMissingStatesComesFromTheDeclaration(t *testing.T) {
	only := filepath.Join("..", "resume", "testdata", "weights-only")
	got := missingStates(only)
	if len(got) == 0 || got[0] != "optimizer" {
		t.Fatalf("missing = %v, want optimizer first", got)
	}
	if full := missingStates(filepath.Join("..", "resume", "testdata", "full")); len(full) != 0 {
		t.Fatalf("a full copy reported %v", full)
	}
	if none := missingStates(t.TempDir()); len(none) != 0 {
		t.Fatalf("an empty directory reported %v", none)
	}
}

// TestAnIncompleteCopyIsEscalatedAfterOneRepair is the loop half: the first answer names the missing
// state, and a newer copy that is still missing it turns a repair into a stop.
func TestAnIncompleteCopyIsEscalatedAfterOneRepair(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("..", "resume", "testdata", "weights-only")
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	l, err := Open(Config{Dir: dir, UnitID: "u", Bounds: policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	s := policy.Snapshot{Copies: []policy.Copy{{Step: 512, State: policy.CopySealed}}}
	l.noteCompleteness(&s)
	if len(s.Copies[0].Missing) == 0 {
		t.Fatal("the declaration was not read")
	}

	// We asked once, at the same copy; a newer copy arrives and is still incomplete.
	l.ledger.Status.IncompleteAskedAt = 512
	newer := policy.Snapshot{Copies: []policy.Copy{{Step: 600, State: policy.CopySealed}}}
	l.noteCompleteness(&newer)
	if newer.IncompleteRepairs != 1 {
		t.Fatalf("IncompleteRepairs = %d, want 1 (the rewrite was answered and did not help)", newer.IncompleteRepairs)
	}
}

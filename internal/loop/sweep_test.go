package loop

import (
	"os"
	"path/filepath"
	"testing"
)

// The sweep is bounded in bytes, and a budget that runs out mid-directory leaves the directory findable for
// the next round. Time was the wrong measure: `os.RemoveAll` cannot be interrupted at all, so a time budget
// overran on the big ones and deleted more than promised on the small ones.
func TestSweepStopsAtTheByteBudget(t *testing.T) {
	dir := t.TempDir()
	trash := filepath.Join(dir, trashDir)

	write := func(dest string, size int) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// One retired copy of 4 KiB, made of four 1 KiB files, and a second copy that must not be touched.
	write(filepath.Join(trash, "step-1", "a"), 1024)
	write(filepath.Join(trash, "step-1", "b"), 1024)
	write(filepath.Join(trash, "step-1", "c"), 1024)
	write(filepath.Join(trash, "step-1", "d"), 1024)
	write(filepath.Join(trash, "step-2", "a"), 1024)

	removed, freed, err := sweepTrash(dir, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if freed != 2048 {
		t.Fatalf("freed %d bytes, want exactly the budget 2048", freed)
	}
	if removed != 0 {
		t.Fatalf("removed %d directories, want 0: step-1 was only half swept", removed)
	}
	// The half-swept directory is still there, so the next round can finish it.
	if _, err := os.Stat(filepath.Join(trash, "step-1")); err != nil {
		t.Fatalf("a half-swept directory vanished: %v", err)
	}

	// A second pass with room finishes both, and reports the directory as gone.
	removed, freed, err = sweepTrash(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed %d directories on the second pass, want 2", removed)
	}
	if freed != 2048+1024 {
		t.Fatalf("freed %d on the second pass, want 3072", freed)
	}
	if _, err := os.Stat(filepath.Join(trash, "step-1")); !os.IsNotExist(err) {
		t.Fatal("step-1 survived a sweep with room to finish it")
	}
}

// Zero means nothing, not everything: it is the value a dry run passes, and sweeping the whole trash on a
// zero budget would turn a preview into the act.
func TestAZeroBudgetSweepsNothing(t *testing.T) {
	dir := t.TempDir()
	trash := filepath.Join(dir, trashDir)
	if err := os.MkdirAll(filepath.Join(trash, "step-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, "step-1", "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, freed, err := sweepTrash(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 || freed != 0 {
		t.Fatalf("a zero budget removed %d dirs and %d bytes", removed, freed)
	}
	if _, err := os.Stat(filepath.Join(trash, "step-1", "a")); err != nil {
		t.Fatal("a zero budget deleted the file anyway")
	}
}

package loop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/resume"
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

// A framework checkpoint is invisible to the observer (it reads our own layout), so the loop has to
// put it into the snapshot itself — otherwise the newest copy looks absent and the missing state never
// reaches a decision.
func TestAFrameworkCheckpointReachesTheSnapshot(t *testing.T) {
	dir := t.TempDir()
	ckpt := filepath.Join(dir, "checkpoint-1000")
	if err := os.MkdirAll(ckpt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ckpt, "trainer_state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resume.Write(ckpt, resume.File{
		Schema: resume.Schema, Generation: 1000, Step: 1000, Format: resume.FormatHFTrainer,
		World:    resume.World{Ranks: 1},
		State:    resume.State{Model: true},
		Requires: []string{"model", "optimizer", "scheduler", "rng"},
	}); err != nil {
		t.Fatal(err)
	}
	l, err := Open(Config{Dir: dir, UnitID: "u", Bounds: policy.Bounds{NoUs: policy.NoUsNone, MinCopies: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	s := policy.Snapshot{} // the observer saw nothing: it only reads our own layout
	l.noteCompleteness(&s)
	if len(s.Copies) != 1 {
		t.Fatalf("snapshot copies = %+v, want the framework copy", s.Copies)
	}
	c := s.Copies[0]
	if c.Step != 1000 || c.Kind != "hf-checkpoint" {
		t.Fatalf("copy = %+v, want step 1000 hf-checkpoint", c)
	}
	want := []string{"optimizer", "scheduler", "rng"}
	if len(c.Missing) != len(want) {
		t.Fatalf("missing = %v, want %v (dataloader is not required)", c.Missing, want)
	}
	for i := range want {
		if c.Missing[i] != want[i] {
			t.Fatalf("missing = %v, want %v", c.Missing, want)
		}
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

	// We asked once, at the same copy; the writer answered with a newer copy, still incomplete. The
	// newer copy exists on disk, which is what makes the answer a fact rather than a simulation.
	newerDir := filepath.Join(dir, "gen-0600")
	if err := os.MkdirAll(newerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newerDir, "manifest.json"),
		[]byte(`{"generation":600,"step":600,"sealedAt":"2026-11-02T04:55:44Z","writer":"ckpttrainer","shards":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resume.Write(newerDir, resume.File{
		Schema: resume.Schema, Generation: 600, Step: 600, Format: resume.FormatDCP,
		World: resume.World{Ranks: 8, DP: 8}, State: resume.State{Model: true},
	}); err != nil {
		t.Fatal(err)
	}

	l.ledger.Status.IncompleteAskedAt = 512
	newer := policy.Snapshot{Copies: []policy.Copy{{Step: 600, State: policy.CopySealed}}}
	l.noteCompleteness(&newer)
	if newer.IncompleteRepairs != 1 {
		t.Fatalf("IncompleteRepairs = %d, want 1 (the rewrite was answered and did not help)", newer.IncompleteRepairs)
	}
}

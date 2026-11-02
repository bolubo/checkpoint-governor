package drill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tree builds a sealed generation with one shard. The drill's questions are about the tree and the
// loader, not about a real checkpoint format.
func tree(t *testing.T) (root, genDir string) {
	t.Helper()
	root = t.TempDir()
	genDir = filepath.Join(root, "gen-0007")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(genDir, "rank-00.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"generation":7,"step":512,"sealedAt":"2026-11-02T03:20:07Z","writer":"test",` +
		`"shards":[{"name":"rank-00.bin","size":7,"sha256":"` + strings.Repeat("a", 64) + `"}]}`
	if err := os.WriteFile(filepath.Join(genDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, genDir
}

func do(t *testing.T, root, genDir, loader string, timeout time.Duration) Report {
	t.Helper()
	rep, err := Do(context.Background(), root, genDir, 7, 512, loader, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestALoaderThatTakesOneStepIsRestorable(t *testing.T) {
	root, genDir := tree(t)
	rep := do(t, root, genDir, "echo step=513", 5*time.Second)
	if !rep.Restorable() {
		t.Fatalf("reason %q, want restorable", rep.Reason)
	}
	if got := rep.Line(); !strings.HasPrefix(got, "resume: ok — step 512 → 513;") {
		t.Fatalf("line %q", got)
	}
	if rep.ExitCode() != 0 {
		t.Fatalf("exit %d, want 0", rep.ExitCode())
	}
}

// The machine-checkable form of "it resumed from scratch": the loader ran, and it reported step 0.
func TestALoaderThatReportsZeroIsNotAResume(t *testing.T) {
	root, genDir := tree(t)
	rep := do(t, root, genDir, "echo step=0", 5*time.Second)
	if rep.Reason != ReasonWrongStep {
		t.Fatalf("reason %q, want wrong-step", rep.Reason)
	}
	if want := "not restorable: the loader reported step 0 (declared 512)"; rep.Line() != want {
		t.Fatalf("line %q, want %q", rep.Line(), want)
	}
	if rep.ExitCode() != 1 {
		t.Fatalf("exit %d, want 1", rep.ExitCode())
	}
}

func TestALoaderThatPrintsNoStepIsNotAResume(t *testing.T) {
	root, genDir := tree(t)
	rep := do(t, root, genDir, "echo hello", 5*time.Second)
	if rep.Reason != ReasonWrongStep {
		t.Fatalf("reason %q, want wrong-step", rep.Reason)
	}
}

// A drill that changed the checkpoint is refused, and refused before the step is judged: a loader that
// "succeeded" by rewriting the evidence has not proven anything.
func TestADrillThatChangesTheCheckpointIsRefused(t *testing.T) {
	root, genDir := tree(t)
	rep := do(t, root, genDir, "printf x >> \"$CKPT_DIR/rank-00.bin\"; echo step=513", 5*time.Second)
	if rep.Reason != ReasonMutated {
		t.Fatalf("reason %q, want mutated", rep.Reason)
	}
	if want := "drill refused: the checkpoint changed during the drill (gen-0007/rank-00.bin changed)"; rep.Line() != want {
		t.Fatalf("line %q, want %q", rep.Line(), want)
	}
	if rep.ExitCode() != 2 {
		t.Fatalf("exit %d, want 2", rep.ExitCode())
	}
}

func TestALoaderThatDoesNotFinishInTimeFails(t *testing.T) {
	root, genDir := tree(t)
	rep := do(t, root, genDir, "sleep 5", 100*time.Millisecond)
	if rep.Reason != ReasonTimeout {
		t.Fatalf("reason %q, want timeout", rep.Reason)
	}
	if !strings.HasPrefix(rep.Line(), "drill failed: the loader did not finish within") {
		t.Fatalf("line %q", rep.Line())
	}
}

func TestALoaderThatExitsNonZeroFails(t *testing.T) {
	root, genDir := tree(t)
	rep := do(t, root, genDir, "echo boom >&2; exit 3", 5*time.Second)
	if rep.Reason != ReasonFailed {
		t.Fatalf("reason %q, want failed", rep.Reason)
	}
	if !strings.Contains(rep.Line(), "the loader exited 3") || !strings.Contains(rep.Line(), "boom") {
		t.Fatalf("line %q does not name the exit code and the last line", rep.Line())
	}
}

// The loader is told where to read through the environment, and nothing else: a loader that needs a
// secret is a loader the drill should not run. This loader fails unless both variables are right.
func TestTheLoaderGetsTheDirectoryAndTheGeneration(t *testing.T) {
	root, genDir := tree(t)
	rep := do(t, root, genDir,
		`test "$CKPT_GEN" = 7 && test -f "$CKPT_DIR/rank-00.bin" && echo step=513`, 5*time.Second)
	if !rep.Restorable() {
		t.Fatalf("reason %q: %s", rep.Reason, rep.Line())
	}
}

func TestASnapshotNoticesAnAddedOrRemovedFile(t *testing.T) {
	root, _ := tree(t)
	before, err := Take(root)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Take(root)
	if err != nil {
		t.Fatal(err)
	}
	if where, changed := before.Difference(after); changed {
		t.Fatalf("two snapshots of an unchanged tree differ: %s", where)
	}
	if err := os.WriteFile(filepath.Join(root, "gen-0007", "late.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := Take(root)
	if err != nil {
		t.Fatal(err)
	}
	where, changed := before.Difference(third)
	if !changed || !strings.Contains(where, "late.bin appeared") {
		t.Fatalf("an added file was not noticed (%q, %v)", where, changed)
	}
}

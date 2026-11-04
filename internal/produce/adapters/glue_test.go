package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGlue(t *testing.T, dir, name, body string) {
	t.Helper()
	base := filepath.Join(dir, ".ckpt-governor")
	path := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A gang: each rank's own step, the slowest one is the unit's progress, and a rank that started but
// never reported is named — never a reason to skip a write.
func TestGangProgressIsTheSlowestRank(t *testing.T) {
	dir := t.TempDir()
	writeGlue(t, dir, "ranks/0/step", "100")
	writeGlue(t, dir, "ranks/1/step", "96")
	writeGlue(t, dir, "ranks/2/step", "98")
	g := ReadGlue(dir)
	if len(g.Members) != 3 {
		t.Fatalf("members = %+v", g.Members)
	}
	min, ok := g.Progress()
	if !ok || min != 96 {
		t.Fatalf("progress = %d, %v; want 96", min, ok)
	}
	if g.Spread() != 4 {
		t.Fatalf("spread = %d, want 4", g.Spread())
	}
	if silent := g.Silent(); len(silent) != 0 {
		t.Fatalf("silent = %v, want none", silent)
	}

	// A rank that exists with no step is silent, and the others still make progress.
	if err := os.Remove(filepath.Join(dir, ".ckpt-governor", "ranks", "1", "step")); err != nil {
		t.Fatal(err)
	}
	g = ReadGlue(dir)
	if silent := g.Silent(); len(silent) != 1 || silent[0] != 1 {
		t.Fatalf("silent = %v, want [1]", silent)
	}
	if min, ok := g.Progress(); !ok || min != 98 {
		t.Fatalf("progress = %d, %v; want 98 (only reporting ranks)", min, ok)
	}
}

// A single-writer job has no `ranks/` directory: every answer stays exactly as it was.
func TestASingleWriterHasNoMembers(t *testing.T) {
	g := ReadGlue(t.TempDir())
	if len(g.Members) != 0 {
		t.Fatalf("members = %+v, want none", g.Members)
	}
	if _, ok := g.Progress(); ok {
		t.Fatal("a single writer reported gang progress")
	}
}

func TestGlueIsEmptyWithoutFiles(t *testing.T) {
	g := ReadGlue(t.TempDir())
	if g.HasExpected || g.HasSeconds || g.StopRequested || g.MayExit {
		t.Fatalf("empty glue = %+v", g)
	}
}

// The four facts in one read, each with its own presence flag.
func TestGlueReadsTheFourFacts(t *testing.T) {
	dir := t.TempDir()
	writeGlue(t, dir, "expected-bytes", "1258291200")
	writeGlue(t, dir, "stopping", "1")
	writeGlue(t, dir, "may-exit", "1")
	writeGlue(t, dir, "seconds-left", "27")

	g := ReadGlue(dir)
	if !g.HasExpected || g.ExpectedBytes != 1258291200 {
		t.Fatalf("expected = %+v", g)
	}
	if !g.StopRequested || !g.MayExit {
		t.Fatalf("stop/exit = %+v", g)
	}
	if !g.HasSeconds || g.SecondsLeft != 27 {
		t.Fatalf("secondsLeft = %+v", g)
	}
}

// The two phases must be distinguishable: a stop request without `may-exit` is a job that still has
// to write, and that is the state the executor must not mistake for "safe to kill".
func TestAStopWithoutMayExitIsNotAnExit(t *testing.T) {
	dir := t.TempDir()
	writeGlue(t, dir, "stopping", "1")
	g := ReadGlue(dir)
	if !g.StopRequested {
		t.Fatal("the stop request was not seen")
	}
	if g.MayExit {
		t.Fatal("a job that has not finished its final write must not look safe to kill")
	}
}

// The third state, and the one the handshake turns on: the job has said training is over (`ending`),
// which is not the same fact as "the platform told it to stop". A reader that conflates them either
// asks a finished run for another checkpoint or releases a stopping one before its last write exists.
func TestEndingIsItsOwnFact(t *testing.T) {
	dir := t.TempDir()
	writeGlue(t, dir, "ending", "1")
	g := ReadGlue(dir)
	if !g.Ending {
		t.Fatal("`ending` was not seen: the finish handshake would never start")
	}
	if g.StopRequested {
		t.Fatal("`ending` was read as a stop request: two different facts, one answer")
	}
}

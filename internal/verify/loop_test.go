package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writer is a stand-in for a checkpoint library: it writes shards, then seals a manifest last. It can be
// told to die part-way through a shard, which is what a kill actually does.
type writer struct {
	dir string
	// shortOn is the generation whose last shard is truncated, and by how much. alwaysShort stands for a
	// writer with a persistent bug rather than one unlucky kill.
	shortOn     int64
	truncate    int64
	alwaysShort bool
	writes      []int64
}

func (w *writer) write(gen int64) error {
	w.writes = append(w.writes, gen)
	genDir := filepath.Join(w.dir, fmt.Sprintf("gen-%04d", gen))
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return err
	}

	shards := []Shard{}
	for i, body := range [][]byte{[]byte("rank 0 weights\n"), []byte("rank 1 weights\n")} {
		name := fmt.Sprintf("rank-%02d.bin", i)
		content := body
		short := (gen == w.shortOn || w.alwaysShort) && i == 1 && w.truncate > 0
		if short {
			content = body[:w.truncate]
		}
		if err := os.WriteFile(filepath.Join(genDir, name), content, 0o644); err != nil {
			return err
		}
		s := Shard{Name: name, Size: int64(len(content)), SHA256: sum(content)}
		s.TailSHA256 = sum(tail(content))
		if short {
			// Sealed as if the whole shard had been written: the manifest lies, which is exactly the
			// state a kill leaves behind when the file is short but the write was planned complete.
			s.Size = int64(len(body))
			s.SHA256 = sum(body)
			s.TailSHA256 = sum(tail(body))
		}
		shards = append(shards, s)
	}

	m := Manifest{Generation: gen, Step: 100 + gen, SealedAt: fixedTime(), Writer: "test-writer", Shards: shards}
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(genDir, "manifest.json"), b, 0o644)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func tail(b []byte) []byte {
	if len(b) > TailBytes {
		return b[len(b)-TailBytes:]
	}
	return b
}

// The product in one test: a checkpoint written short is refused, a rewrite is asked for as a **new**
// generation, and at no point in between is anything recorded as verified.
func TestAShortCopyIsRefusedAndReplaced(t *testing.T) {
	dir := t.TempDir()
	w := &writer{dir: dir, shortOn: 7, truncate: 3}

	budget := Budget{Attempts: 3, Window: time.Minute}
	now := fixedTime()
	var progress Progress
	want := int64(7)

	// Round 1: the writer dies mid-shard, sealing a manifest that claims more than it wrote.
	if err := w.write(want); err != nil {
		t.Fatal(err)
	}
	v := Classify(filepath.Join(dir, "gen-0007"), want, Options{Now: now, CheckContent: true})
	if v.Reason != ReasonShort {
		t.Fatalf("round 1: %s/%s (%s), want a short copy", v.State, v.Reason, v.Detail)
	}
	d := Next(v, want, progress, budget, now)
	if d.Action != ActionRewrite || d.Generation != 8 {
		t.Fatalf("round 1 decision = %s gen %d, want rewrite gen 8", d.Action, d.Generation)
	}

	// Round 2: the rewrite lands whole, at the generation we asked for.
	progress.Attempts++
	if progress.FirstAt.IsZero() {
		progress.FirstAt = now
	}
	want = d.Generation
	if err := w.write(want); err != nil {
		t.Fatal(err)
	}
	v = Classify(filepath.Join(dir, "gen-0008"), want, Options{Now: now.Add(time.Second), CheckContent: true})
	if v.State != "verified" {
		t.Fatalf("round 2: %s/%s (%s), want verified", v.State, v.Reason, v.Detail)
	}
	if d := Next(v, want, progress, budget, now.Add(time.Second)); d.Action != ActionDone {
		t.Fatalf("round 2 decision = %s, want done", d.Action)
	}

	// The generation we replaced is still on disk, still short, and can never be mistaken for an answer:
	// a request for 8 judges it stale before it judges it short.
	old := Classify(filepath.Join(dir, "gen-0007"), 8, Options{Now: now, CheckContent: true})
	if old.Reason != ReasonStale {
		t.Fatalf("the replaced copy against the new request: %s, want stale", old.Reason)
	}

	if len(w.writes) != 2 {
		t.Fatalf("the writer was asked to write %v, want exactly two generations", w.writes)
	}
}

// A writer that keeps failing is not retried forever: the budget is what turns a bad checkpoint into a
// report instead of a busy volume.
func TestAWriterThatNeverSucceedsIsGivenUpOn(t *testing.T) {
	dir := t.TempDir()
	w := &writer{dir: dir, truncate: 3, alwaysShort: true}
	budget := Budget{Attempts: 2, Window: time.Minute}
	now := fixedTime()

	var progress Progress
	want := int64(7)
	for i := 0; i < 5; i++ {
		if err := w.write(want); err != nil {
			t.Fatal(err)
		}
		v := Classify(filepath.Join(dir, fmt.Sprintf("gen-%04d", want)), want,
			Options{Now: now, CheckContent: true})
		d := Next(v, want, progress, budget, now)
		if d.Action == ActionDone {
			t.Fatal("a short copy was reported done")
		}
		if d.Action == ActionGiveUp {
			if progress.Attempts != budget.Attempts {
				t.Fatalf("gave up after %d attempts, budget was %d", progress.Attempts, budget.Attempts)
			}
			if len(w.writes) != budget.Attempts+1 {
				t.Fatalf("wrote %d generations for a budget of %d attempts", len(w.writes), budget.Attempts)
			}
			return
		}
		progress.Attempts++
		if progress.FirstAt.IsZero() {
			progress.FirstAt = now
		}
		want = d.Generation
	}
	t.Fatal("never gave up: the loop is unbounded")
}

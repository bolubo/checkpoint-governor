package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bolubo/checkpoint-governor/internal/policy"
	"github.com/bolubo/checkpoint-governor/internal/verify"
)

// The seam, on every commit: the writer in this package writes, and the verifier in another package reads.
//
// It exists because the two disagreed for a whole day without either side's tests failing. Every fixture in
// internal/verify was a tree its author had designed, and every test here wrote through its own assumptions;
// the moment the writer changed its directory naming, the reader stopped seeing checkpoints and nothing went
// red. A test that crosses the boundary with the real writer is the only kind that can notice.
func TestWhatThisWriterWritesIsWhatTheVerifierCanJudge(t *testing.T) {
	dir := t.TempDir()

	c := config{
		dir:        dir,
		steps:      3,
		stepMS:     10,
		writeMS:    20,
		shardBytes: 4096,
		ranks:      2,
		contract:   true,
		saveEvery:  20 * time.Millisecond,
	}
	if err := runWith(c, nil); err != nil {
		t.Fatalf("the writer failed: %v", err)
	}

	// Whatever it left, the verifier must be able to name it — and it must not be "nothing there".
	s, err := verify.Summarize(dir, 0, verify.Options{Now: time.Now(), CheckContent: true})
	if err != nil {
		t.Fatalf("the verifier could not read the directory the writer made: %v", err)
	}
	if len(s.Receipts) == 0 {
		t.Fatal("the writer ran and the verifier saw no generations: the two disagree about the layout")
	}
	if len(s.Verdicts) == 0 || s.Verdicts[len(s.Verdicts)-1].State != policy.CopyVerified {
		t.Fatalf("the last generation was not verified: %+v", s.Verdicts)
	}
}

// The receipt's counter (internal/loop) counts directories that start with  or  and hold a
// manifest. Assert those two facts directly: it is what the counter needs to be right, and asserting them
// here keeps the seam honest without duplicating its code.
func TestTheContractLayoutIsOneTheCounterRecognises(t *testing.T) {
	dir := t.TempDir()

	c := config{
		dir: dir, steps: 6, stepMS: 10, writeMS: 20, shardBytes: 4096, ranks: 1,
		contract: true, saveEvery: 20 * time.Millisecond,
	}
	if err := runWith(c, nil); err != nil {
		t.Fatalf("the writer failed: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	generations := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !(strings.HasPrefix(name, "step-") || strings.HasPrefix(name, "gen-")) {
			continue
		}
		generations++
		if _, err := os.Stat(filepath.Join(dir, name, "manifest.json")); err != nil {
			t.Fatalf("%s has no manifest: the counter would not count it, and a reader would call it in flight", name)
		}
	}
	if generations == 0 {
		t.Fatal("the writer produced no directory either layout recognises")
	}
}

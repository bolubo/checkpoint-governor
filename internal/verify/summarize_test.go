package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A root walks oldest generation first, and a generation that is still being written does not count as a
// failure.
func TestSummarizeWalksGenerationsInOrder(t *testing.T) {
	root := t.TempDir()

	// gen-9 and gen-10 are here to pin the ordering: as strings, gen-10 sorts before gen-9.
	for _, name := range []string{"gen-0009", "gen-0010"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rank-00.bin"), []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s, err := Summarize(root, 0, Options{Now: fixedTime(), CheckContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Receipts) != 2 {
		t.Fatalf("%d generations, want 2", len(s.Receipts))
	}
	if s.Receipts[0].Generation != 9 || s.Receipts[1].Generation != 10 {
		t.Fatalf("order = %d,%d, want 9,10", s.Receipts[0].Generation, s.Receipts[1].Generation)
	}
	if s.Tally.Unchecked != 2 {
		t.Fatalf("a generation with no manifest counted as %d unchecked, want 2", s.Tally.Unchecked)
	}
	if s.Tally.Invalid != 0 {
		t.Fatal("a write in flight was counted as invalid")
	}
}

// The lines a person reads, and the numbers that decide whether this was worth running.
func TestSummarizePrintsALineAndATally(t *testing.T) {
	root := filepath.Join("testdata", "sealed-ok")
	s, err := Summarize(root, 7, Options{Now: fixedTime(), CheckContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Receipts) != 1 {
		t.Fatalf("%d generations, want 1", len(s.Receipts))
	}
	line := s.Receipts[0].Line()
	if !strings.HasPrefix(line, "generation 7") || !strings.Contains(line, "sealed") {
		t.Fatalf("line = %q", line)
	}
	if s.Tally.Verified != 1 || s.Tally.InvalidRate() != 0 {
		t.Fatalf("tally = %+v, want one verified and a zero invalid rate", s.Tally)
	}
}

// A root that does not exist is an error, not an empty summary: "no generations" and "no directory" are
// different facts, and only one of them means nothing is wrong.
func TestSummarizeRefusesAMissingRoot(t *testing.T) {
	if _, err := Summarize(filepath.Join(t.TempDir(), "absent"), 0, Options{Now: time.Now()}); err == nil {
		t.Fatal("a missing root summarised as if it were empty")
	}
}

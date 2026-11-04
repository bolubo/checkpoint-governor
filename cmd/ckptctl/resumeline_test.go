package main

import (
	"strings"
	"testing"
)

// TestTheReceiptSaysWhetherTheNewestCopyCanContinue is the receipt line's acceptance: a weights-only
// copy must say so, and a directory with nothing sealed must stay silent.
func TestTheReceiptSaysWhetherTheNewestCopyCanContinue(t *testing.T) {
	got := ResumeLine(fixture(t, "weights-only"))
	// The failure this line exists for: the bytes are whole, and the run cannot continue.
	if !strings.Contains(got, "not resumable: optimizer state absent") {
		t.Fatalf("receipt line = %q, want it to name the missing optimizer state", got)
	}
	t.Log(got)

	if line := ResumeLine(t.TempDir()); line != "" {
		t.Fatalf("an empty directory printed %q, want silence", line)
	}
}

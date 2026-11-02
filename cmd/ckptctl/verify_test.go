package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// The command answers with the state first, so a person can grep it, and the detail second, so they can
// act on it.
func TestVerifyCommandReportsTheStateAndWhy(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "verify", "testdata", "sealed-ok", "gen-0007")

	var out bytes.Buffer
	if code := runVerify([]string{"--dir", dir, "--generation", "7", "--full"}, &out); code != 0 {
		t.Fatalf("exit %d for a good copy: %s", code, out.String())
	}
	got := out.String()
	if !strings.HasPrefix(got, "verified ok") {
		t.Fatalf("output = %q, want it to start with `verified ok`", got)
	}
	if !strings.Contains(got, "steps 48210") {
		t.Fatalf("output = %q, want the step", got)
	}
}

// A copy that is not good enough is a failure for whatever runs this, and it says which shard.
func TestVerifyCommandFailsOnAShortCopy(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "verify", "testdata", "short-shard", "gen-0007")

	var out bytes.Buffer
	if code := runVerify([]string{"--dir", dir, "--generation", "7"}, &out); code != 1 {
		t.Fatalf("exit %d for a short copy, want 1: %s", code, out.String())
	}
	got := out.String()
	if !strings.HasPrefix(got, "broken short") {
		t.Fatalf("output = %q, want `broken short`", got)
	}
	if !strings.Contains(got, "rank-") {
		t.Fatalf("output = %q, want the shard named", got)
	}
}

// A write still in flight is not a failure: the command exists to be run by a loop, and a loop that treats
// "not finished yet" as an error rewrites a checkpoint that is being written.
func TestVerifyCommandDoesNotFailACopyStillInFlight(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "verify", "testdata", "no-manifest", "gen-0007")

	var out bytes.Buffer
	if code := runVerify([]string{"--dir", dir, "--generation", "7"}, &out); code != 0 {
		t.Fatalf("exit %d for a copy in flight: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "waiting") {
		t.Fatalf("output = %q, want waiting", out.String())
	}
}

// Without a directory there is nothing to say, and guessing one would be worse than saying so.
func TestVerifyCommandNeedsADirectory(t *testing.T) {
	var out bytes.Buffer
	if code := runVerify(nil, &out); code != 2 {
		t.Fatalf("exit %d with no --dir, want 2", code)
	}
}

// Walking a root prints one line per generation and the numbers, and a broken generation fails the command
// so a script notices without parsing prose.
func TestVerifyAllWalksARoot(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "verify", "testdata", "short-shard")

	var out bytes.Buffer
	code := runVerify([]string{"--dir", dir, "--all"}, &out)
	if code != 1 {
		t.Fatalf("exit %d for a root holding a short copy, want 1", code)
	}
	got := out.String()
	if !strings.Contains(got, "generation 7") {
		t.Fatalf("output = %q, want a line for generation 7", got)
	}
	if !strings.Contains(got, "invalid rate") {
		t.Fatalf("output = %q, want the invalid rate", got)
	}
}

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/advise"
)

// The schema is the one promise a person can check before trusting a directory, so doctor states it
// first: which version wrote the plan, and which versions this binary can read.
func TestDoctorPrintsThePlanSchema(t *testing.T) {
	dir := t.TempDir()
	if code := runRun([]string{"--dir=" + dir, "--once", "--no-us=none"}, io.Discard); code != 0 {
		t.Fatalf("run --once exited %d", code)
	}

	var out strings.Builder
	if code := runDoctor([]string{"--dir", dir}, &out); code != 0 {
		t.Fatalf("doctor exited %d:\n%s", code, out.String())
	}
	want := "plan schema 2, this binary reads 2"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("doctor does not state the plan's schema (%q):\n%s", want, out.String())
	}

	// No plan is a normal directory, and the line still says what this binary would read.
	var empty strings.Builder
	if code := runDoctor([]string{"--dir", t.TempDir()}, &empty); code != 0 {
		t.Fatalf("doctor on an empty directory exited %d", code)
	}
	if !strings.Contains(empty.String(), "plan schema: none (this binary reads 2)") {
		t.Fatalf("doctor does not say there is no plan:\n%s", empty.String())
	}
}

// A plan from another version is refused, with its number named, and the exit code says so: a
// script must not read a table of environment facts as a verdict about the directory.
func TestDoctorRefusesAPlanFromAnotherSchema(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, advise.BaseDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := `{"schema": 99, "epoch": 7, "unitId": "job-1", "action": "skip", "expiresAt": 1}`
	if err := os.WriteFile(filepath.Join(base, "state.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := runDoctor([]string{"--dir", dir}, &out); code != 2 {
		t.Fatalf("doctor exited %d, want 2:\n%s", code, out.String())
	}
	for _, want := range []string{"plan schema 99, this binary reads 2", "refused", "schema 99"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor does not print %q:\n%s", want, out.String())
		}
	}
}

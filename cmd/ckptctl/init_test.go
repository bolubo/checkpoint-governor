package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/advise"
)

// Skipping writes nothing at all. Decision in product-v1 §13: "zero config" has to keep
// meaning what it says, and a file saying `no-us=none` would make the next run look configured when
// nobody decided anything.
func TestInitWritesNothingWhenTheAnswerIsNone(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if code := runInit([]string{"--dir", dir, "--duration", "4h", "--answer", "none"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, advise.BaseDir, advise.NumbersFile)); err == nil {
		t.Fatal("init wrote a configuration file for an answer of none")
	}
	if !strings.Contains(out.String(), "nothing written") {
		t.Fatalf("output = %q", out.String())
	}
}

// The middle answer is one write at half the run, and it has to be readable by the run afterwards.
func TestInitWritesWithUsForTheMiddleAnswer(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if code := runInit([]string{"--dir", dir, "--duration", "4h", "--answer", "middle"}, &out); code != 0 {
		t.Fatalf("exit = %d:\n%s", code, out.String())
	}
	n, err := advise.LoadNumbers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n.WithUs != "2h" {
		t.Fatalf("with-us = %q, want 2h (half of the stated run)", n.WithUs)
	}
}

// The three prices are printed whether or not a terminal is attached: a scripted install still needs
// to know what the options cost, and it is the only place the numbers are stated before a run.
func TestInitPrintsThreePrices(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, advise.BaseDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, advise.BaseDir, "expected-bytes"), []byte("2147483648"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	runInit([]string{"--dir", dir, "--duration", "4h"}, &out)
	for _, want := range []string{"none", "middle", "periodic", "a silent death costs"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the price sheet is missing %q:\n%s", want, out.String())
		}
	}
}

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

// A model size is priced in disk and nothing else: 18 bytes per parameter, with the provenance
// printed beside the number. The four sizes are the ones the design pins.
func TestInitParams(t *testing.T) {
	cases := []struct{ params, size string }{
		{"1.5B", "25 GiB"},
		{"7B", "117 GiB"},
		{"70B", "1.17 TiB"},
		{"175B", "2.93 TiB"},
	}
	for _, c := range cases {
		t.Run(c.params, func(t *testing.T) {
			var out strings.Builder
			if code := runInit([]string{"--dir", t.TempDir(), "--params", c.params,
				"--duration", "4h", "--answer", "periodic"}, &out); code != 0 {
				t.Fatalf("exit = %d:\n%s", code, out.String())
			}
			text := out.String()
			if !strings.Contains(text, c.size) {
				t.Fatalf("no %s for %s:\n%s", c.size, c.params, text)
			}
			for _, want := range []string{"estimated from " + c.params, "not measured", "not a GPU check"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q:\n%s", want, text)
				}
			}
			// Every answer has to carry its disk cost, not only the one the user picked.
			for _, key := range []string{"none", "middle", "periodic"} {
				var found bool
				for _, line := range strings.Split(text, "\n") {
					if strings.HasPrefix(line, "  "+key+" ") {
						found = true
						if !strings.Contains(line, "disk:") {
							t.Fatalf("the %s row has no disk cost: %q", key, line)
						}
					}
				}
				if !found {
					t.Fatalf("no %s row:\n%s", key, text)
				}
			}
		})
	}
}

// "7" is seven what? A number without a unit must not become a disk estimate.
func TestInitRefusesParamsWithoutAUnit(t *testing.T) {
	var out strings.Builder
	if code := runInit([]string{"--dir", t.TempDir(), "--params", "7", "--answer", "none"}, &out); code != 2 {
		t.Fatalf("exit = %d, want 2:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "write 7B or 700M") {
		t.Fatalf("the refusal does not say how to write it:\n%s", out.String())
	}
}

// Before anything is written and without a declared size, disk is unknown — and says so, rather than
// printing a zero a person might budget against.
func TestInitSaysDiskIsUnknownBeforeTheFirstWrite(t *testing.T) {
	var out strings.Builder
	if code := runInit([]string{"--dir", t.TempDir(), "--duration", "4h", "--answer", "periodic"}, &out); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "disk: unknown until the first write") {
		t.Fatalf("disk is not declared unknown:\n%s", out.String())
	}
}

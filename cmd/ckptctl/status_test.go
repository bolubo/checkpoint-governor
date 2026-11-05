package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bolubo/checkpoint-governor/internal/advise"
	"github.com/bolubo/checkpoint-governor/internal/resume"
)

func TestStatusReadsTheLedger(t *testing.T) {
	dir := t.TempDir()
	l, err := advise.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Status = advise.Status{UnitID: "train/llama", Generation: 2, Phase: advise.PhaseIdle}
	if err := l.Save(dir); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := runStatus([]string{dir}, &out); code != 0 {
		t.Fatalf("status exited %d", code)
	}
	if !strings.Contains(out.String(), "phase idle") || !strings.Contains(out.String(), "generation 2") {
		t.Fatalf("the view is missing the state:\n%s", out.String())
	}
}

func TestStatusSaysWhenNothingHasBeenGoverned(t *testing.T) {
	var out strings.Builder
	if code := runStatus([]string{t.TempDir()}, &out); code != 0 {
		t.Fatalf("status exited %d", code)
	}
	if !strings.Contains(out.String(), "has not been governed") {
		t.Fatalf("a directory with no ledger must say so:\n%s", out.String())
	}
}

// A declaration may carry an extension, and a reader that ignores it must say that it did — otherwise
// the operator cannot tell "nothing there" from "this build did not look". The values stay in the file:
// a status line is not a place to echo untrusted content.
func TestStatusPrintsExtKeys(t *testing.T) {
	dir := t.TempDir()
	l, err := advise.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Status = advise.Status{UnitID: "train/llama", Generation: 3, Phase: advise.PhaseIdle}
	if err := l.Save(dir); err != nil {
		t.Fatal(err)
	}

	gen := filepath.Join(dir, "gen-3")
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen, "manifest.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resume.Write(gen, resume.File{
		Schema: resume.Schema, Generation: 3, Step: 3, Format: resume.FormatOther,
		World:    resume.World{Ranks: 1},
		State:    resume.State{Model: true},
		Requires: []string{"model"},
		Ext:      map[string]json.RawMessage{"requires-v2": json.RawMessage(`{"secret":"carried"}`)},
	}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := runStatus([]string{dir}, &out); code != 0 {
		t.Fatalf("status exited %d", code)
	}
	if !strings.Contains(out.String(), "ext: requires-v2") {
		t.Fatalf("the extension is not named:\n%s", out.String())
	}
	if strings.Contains(out.String(), "carried") {
		t.Fatalf("the extension's value was echoed:\n%s", out.String())
	}

	var js strings.Builder
	if code := runStatus([]string{"--json", dir}, &js); code != 0 {
		t.Fatalf("status --json exited %d", code)
	}
	var payload struct {
		Ext []string `json:"ext"`
	}
	if err := json.Unmarshal([]byte(js.String()), &payload); err != nil {
		t.Fatalf("json: %v\n%s", err, js.String())
	}
	if len(payload.Ext) != 1 || payload.Ext[0] != "requires-v2" {
		t.Fatalf("json ext = %v, want [requires-v2]", payload.Ext)
	}
}

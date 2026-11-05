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

// A job's own final write can land after the last ledger round. Status must read the directory, not
// keep saying `phase writing` about a copy that is already sealed (the S2 user run on 2026-11-05).
func TestStatusClosesAWriteTheDirectorySealed(t *testing.T) {
	dir := t.TempDir()
	l, err := advise.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Status = advise.Status{UnitID: "job-1", Generation: 1, Phase: advise.PhaseWriting,
		WriteStartedAt: 100}
	if err := l.Save(dir); err != nil {
		t.Fatal(err)
	}

	gen := filepath.Join(dir, "gen-0001")
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gen, "manifest.json"), []byte(`{"generation":1,"step":300,"sealedAt":"2026-11-05T10:53:14Z","writer":"x","shards":[{"name":"rank0.pt","size":1,"sha256":"`+strings.Repeat("a", 64)+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := resume.Write(gen, resume.File{
		Schema: resume.Schema, Generation: 1, Step: 300, Format: resume.FormatOther,
		World: resume.World{Ranks: 1},
		State: resume.State{Model: true},
	}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if code := runStatus([]string{dir}, &out); code != 0 {
		t.Fatalf("status exited %d", code)
	}
	got := out.String()
	if !strings.Contains(got, "phase sealed") || !strings.Contains(got, "newest sealed   step 300") {
		t.Fatalf("the sealed copy is not shown:\n%s", got)
	}
	if strings.Contains(got, "writing         step 0") {
		t.Fatalf("a sealed write is still printed as in flight:\n%s", got)
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

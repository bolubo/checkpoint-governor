package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Declaring is the manual glue: a copy that existed before the integration did can still become known.
func TestDeclareTurnsAnUnknownCopyIntoAResumableOne(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "checkpoint-1000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trainer_state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runResumable([]string{"--dir=" + root}, &out); code != 2 {
		t.Fatalf("before declare: exit %d, want 2: %s", code, out.String())
	}

	out.Reset()
	if code := runDeclare([]string{"--dir=" + dir, "--step=1000", "--format=hf-trainer"}, &out); code != 0 {
		t.Fatalf("declare: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "declared resume.json (step 1000, requires model, optimizer, scheduler, rng)") {
		t.Fatalf("declare said %q", out.String())
	}

	out.Reset()
	if code := runResumable([]string{"--dir=" + root}, &out); code != 0 {
		t.Fatalf("after declare: exit %d, want 0: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "resumable (step 1000, requires") {
		t.Fatalf("after declare: %q", out.String())
	}
}

// An artifact is declared beside it, with the file named as the target: the only two places allowed.
func TestDeclareBesideAnArtifact(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "last.ckpt")
	if err := os.WriteFile(artifact, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runDeclare([]string{"--dir=" + artifact, "--step=40", "--state=model,optimizer,scheduler,rng,dataloader", "--requires=model,optimizer,scheduler,rng,dataloader"}, &out); code != 0 {
		t.Fatalf("declare: exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(artifact + ".resume.json"); err != nil {
		t.Fatalf("declaration was not written beside the artifact: %v", err)
	}

	out.Reset()
	if code := runResumable([]string{"--dir=" + root}, &out); code != 0 {
		t.Fatalf("after declare: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "resumable (step 40, requires model, optimizer, scheduler, rng, dataloader)") {
		t.Fatalf("after declare: %q", out.String())
	}
}

// A multi-rank copy can say how many ranks wrote it. Without this, a copy written by 8 ranks and read
// by 8 looked like a world change (1 → 8) and was refused as unresumable.
func TestDeclareWorld(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "checkpoint-1000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trainer_state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runDeclare([]string{"--dir=" + dir, "--step=1000", "--world=8"}, &out); code != 0 {
		t.Fatalf("declare: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := runResumable([]string{"--dir=" + root, "--world=8"}, &out); code != 0 {
		t.Fatalf("resumable: exit %d, want 0: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "resumable (step 1000, requires") {
		t.Fatalf("resumable said %q", out.String())
	}

	// The default is still the single-writer answer: no flag, no world claim.
	out.Reset()
	if code := runDeclare([]string{"--dir=" + dir, "--step=1000"}, &out); code != 0 {
		t.Fatalf("declare default: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := runResumable([]string{"--dir=" + root, "--world=8"}, &out); code != 1 {
		t.Fatalf("resumable after a single-writer declare: exit %d, want 1 (world changed): %s", code, out.String())
	}
}

// A world a declaration cannot mean is refused at the flag, not written and judged later.
func TestDeclareRefusesAnImpossibleWorld(t *testing.T) {
	var out bytes.Buffer
	if code := runDeclare([]string{"--dir=" + t.TempDir(), "--step=1", "--world=0"}, &out); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "world") {
		t.Fatalf("declare said %q", out.String())
	}
}
func TestDeclareRefusesAStateItCannotName(t *testing.T) {
	var out bytes.Buffer
	if code := runDeclare([]string{"--dir=" + t.TempDir(), "--step=1", "--state=weights"}, &out); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"weights" is not a state`) {
		t.Fatalf("declare said %q", out.String())
	}
}

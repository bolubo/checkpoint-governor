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

// A typo in a flag is refused, not silently dropped.
func TestDeclareRefusesAStateItCannotName(t *testing.T) {
	var out bytes.Buffer
	if code := runDeclare([]string{"--dir=" + t.TempDir(), "--step=1", "--state=weights"}, &out); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"weights" is not a state`) {
		t.Fatalf("declare said %q", out.String())
	}
}

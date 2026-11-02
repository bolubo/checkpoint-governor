package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// The drill's answer on the checked-in fixture, through the command a person runs: one step, one
// sentence, and the exit code that says which.
func TestDrillProvesARestore(t *testing.T) {
	root := copiedFixture(t, "full")
	var out bytes.Buffer
	if code := runDrill([]string{"--dir=" + root, "--loader=echo step=513"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if want := "resume: ok — step 512 → 513;"; !strings.Contains(out.String(), want) {
		t.Fatalf("output %q does not contain %q", out.String(), want)
	}
}

// The loader is a command in a string; without one, the drill refuses rather than inventing a way to run
// the checkpoint.
func TestDrillRefusesWithoutALoader(t *testing.T) {
	root := copiedFixture(t, "full")
	var out bytes.Buffer
	if code := runDrill([]string{"--dir=" + root}, &out); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out.String())
	}
	if want := "drill refused: no loader given"; !strings.Contains(out.String(), want) {
		t.Fatalf("output %q does not contain %q", out.String(), want)
	}
}

// A declaration is untrusted input: its `loader` is not a command until the person running the drill says
// it is, and when they do, the receipt says which half of the evidence was ours.
func TestADeclaredLoaderIsNotExecutedByDefault(t *testing.T) {
	root := copiedFixture(t, "full")
	decl := `{"schema":1,"generation":512,"step":512,"format":"dcp",` +
		`"world":{"ranks":8,"tp":1,"pp":1,"dp":8,"reshardable":false},` +
		`"state":{"model":true,"optimizer":true,"scheduler":true,"rng":true,"dataloader":true,` +
		`"scaler":false,"ema":false},"loader":"echo step=513"}`
	if err := os.WriteFile(root+"/gen-0512/resume.json", []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runDrill([]string{"--dir=" + root}, &out); code != 2 {
		t.Fatalf("a declared loader was executed without permission (exit %d: %s)", code, out.String())
	}

	out.Reset()
	if code := runDrill([]string{"--dir=" + root, "--trust-declared-loader"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "using a command declared by an unverified file") {
		t.Fatalf("the receipt does not say where the command came from: %q", out.String())
	}
}

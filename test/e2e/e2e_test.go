//go:build e2e

// Package e2e drives the shipped binary the way a user does: a real directory, a real world file,
// real processes, and the plan the job would read. The shell experiments prove the protocol; this
// proves that the thing in the image still does it after a refactor.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binary string

// TestMain builds the binary once. A test that shells out to `go run` spends its budget compiling.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ckpt-e2e")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "ckptctl")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/ckptctl")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func ckpt(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	return out.String(), code
}

type plan struct {
	Schema int    `json:"schema"`
	Action string `json:"action"`
	Copies int    `json:"copiesToWrite"`
	Reason string `json:"reason"`
}

func readPlan(t *testing.T, dir string) (plan, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".ckpt-governor", "state.json"))
	if err != nil {
		return plan{}, false
	}
	var p plan
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("the plan is not JSON: %v", err)
	}
	return p, true
}

// A preemption announcement is the one fact the whole design is built around. After it, the plan the
// job reads must ask for a write — or say plainly that no write fits. A plan that says `skip` after
// the platform has already decided to take the machine is the failure this product exists to prevent.
func TestPreemptionAsksForAWrite(t *testing.T) {
	dir := t.TempDir()

	if out, code := ckpt(t, "world", "--dir", dir, "--cluster", "a", "--unit", dir, "--fake", "--announce"); code != 0 {
		t.Fatalf("world --fake --announce: exit %d\n%s", code, out)
	}

	out, code := ckpt(t, "run", "--dir", dir, "--once")
	if code != 0 {
		t.Fatalf("run --once: exit %d\n%s", code, out)
	}

	p, ok := readPlan(t, dir)
	if !ok {
		t.Fatalf("no plan was published\n%s", out)
	}
	if p.Schema != 2 {
		t.Errorf("plan schema is %d, the binary writes 2", p.Schema)
	}
	switch p.Action {
	case "save":
		if p.Copies < 1 {
			t.Errorf("a save plan asks for %d copies", p.Copies)
		}
	case "infeasible", "repair":
		// A write that does not fit is an honest answer; it must still say so in the plan.
		if p.Reason == "" {
			t.Errorf("action %q with no reason: the job cannot act on it", p.Action)
		}
	case "skip":
		t.Errorf("the platform announced a preemption and the plan says skip (reason %q)", p.Reason)
	default:
		t.Errorf("unexpected action %q", p.Action)
	}
}

// The other end of the same contract: a plan this binary cannot read is reported, not silently
// replaced by "no plan". The exit code matters — a script has to be able to tell the two apart.
func TestAForeignPlanIsRefusedOutLoud(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, ".ckpt-governor")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := []byte(`{"schema": 1, "epoch": 3, "fallbackIntervalSeconds": 600, "action": "save"}`)
	if err := os.WriteFile(filepath.Join(base, "state.json"), foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := ckpt(t, "explain", "--dir", dir)
	if code == 0 {
		t.Fatalf("explain accepted a schema-1 plan:\n%s", out)
	}
	if !strings.Contains(out, "schema 1") || !strings.Contains(out, "writes 2") {
		t.Fatalf("the refusal must name both versions, got:\n%s", out)
	}

	if _, ok := readPlan(t, dir); !ok {
		t.Fatal("the test lost its own file")
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "internal", "resume", "testdata", name)
}

// One row per sentence of the contract, run through the command a person runs.
func TestResumablePrintsTheVerdictAndTheExitCode(t *testing.T) {
	cases := []struct {
		dir  string
		args []string
		want string
		code int
	}{
		{"full", nil, "resumable (gen 512, step 512, world 8/8, full state)", 0},
		{"weights-only", nil, "not resumable: optimizer state absent", 1},
		{"partial-optimizer", nil, "not resumable: declared partial (optimizer)", 1},
		{"reshardable", []string{"--world=16"}, "resumable with resharding (8 → 16 ranks)", 0},
		{"full", []string{"--world=16"}, "not resumable: world changed 8 → 16", 1},
		{"unknown-format", nil, `unknown: resume.json: format "nemo"`, 2},
		{"no-resume", nil, "unknown: sealed, but nobody said what it holds", 2},
		{"invalid", nil, "unknown: resume.json:", 2},
	}
	for _, c := range cases {
		t.Run(c.dir+strings.Join(c.args, ""), func(t *testing.T) {
			var out bytes.Buffer
			args := append([]string{"--dir=" + fixture(t, c.dir)}, c.args...)
			code := runResumable(args, &out)
			if code != c.code {
				t.Fatalf("exit %d, want %d: %s", code, c.code, out.String())
			}
			if !strings.Contains(out.String(), c.want) {
				t.Fatalf("output %q does not contain %q", out.String(), c.want)
			}
		})
	}
}

// Nothing sealed yet is not a bad checkpoint: it is a write that has not finished, and the command says
// so with exit 2 rather than blaming the writer.
func TestResumableOnAnEmptyDirectoryCannotDecide(t *testing.T) {
	var out bytes.Buffer
	if code := runResumable([]string{"--dir=" + t.TempDir()}, &out); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "nothing sealed yet") {
		t.Fatalf("output %q does not say there is nothing sealed", out.String())
	}
}

// copiedFixture copies a checked-in tree into a temp directory, so a test can write the job's own glue
// without touching the fixture.
func copiedFixture(t *testing.T, name string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(fixture(t, name))); err != nil {
		t.Fatal(err)
	}
	return dst
}

func writeGlue(t *testing.T, root, name, value string) {
	t.Helper()
	dir := filepath.Join(root, ".ckpt-governor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResumedReportsWhatWasAdopted(t *testing.T) {
	t.Run("adopted with the load time", func(t *testing.T) {
		root := copiedFixture(t, "full")
		writeGlue(t, root, "resumed-generation", "512")
		writeGlue(t, root, "resumed-step", "512")
		writeGlue(t, root, "load-seconds", "9.1")

		var out bytes.Buffer
		code := runResumed([]string{"--dir=" + root}, &out)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		if want := "adopted gen 512 (step 512), load 9.1s"; !strings.Contains(out.String(), want) {
			t.Fatalf("output %q does not contain %q", out.String(), want)
		}
	})

	// The sentence this command exists for: a process that came back and started at step 0.
	t.Run("cold start", func(t *testing.T) {
		root := copiedFixture(t, "full")
		writeGlue(t, root, "step", "3")

		var out bytes.Buffer
		code := runResumed([]string{"--dir=" + root}, &out)
		if code != 1 {
			t.Fatalf("exit %d, want 1: %s", code, out.String())
		}
		if want := "not adopted: step 3, newest sealed 512"; !strings.Contains(out.String(), want) {
			t.Fatalf("output %q does not contain %q", out.String(), want)
		}
	})
}

// A verdict is a report, not an input (decision 0026). The same work, with the declaration changed from
// "resumable" to "we cannot judge", must publish the same decision: if a bad copy could suppress a
// write, the loop would grow a feedback path that no number would show.
func TestResumabilityNeverChangesADecision(t *testing.T) {
	good := copiedFixture(t, "full")
	bad := copiedFixture(t, "full")
	// A writer that invented a format: `bad` is `unknown`, `good` is `resumable`.
	foreign := []byte(`{"schema":1,"generation":512,"step":512,"format":"nemo",` +
		`"world":{"ranks":8,"tp":1,"pp":1,"dp":8,"reshardable":false},` +
		`"state":{"model":true,"optimizer":true,"scheduler":true,"rng":true,"dataloader":true,` +
		`"scaler":false,"ema":false}}`)
	if err := os.WriteFile(filepath.Join(bad, "gen-0512", "resume.json"), foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{good, bad} {
		if code := runRun([]string{"--dir=" + root, "--unit=job-1", "--once", "--no-us=none"}, io.Discard); code != 0 {
			t.Fatalf("run --once on %s: exit %d", root, code)
		}
	}

	// The two verdicts have to differ, or the test proves nothing.
	goodVerdict, badVerdict := verdictOf(t, good), verdictOf(t, bad)
	if !strings.HasPrefix(goodVerdict, "resumable") {
		t.Fatalf("good copy is %q, want resumable", goodVerdict)
	}
	if !strings.HasPrefix(badVerdict, "unknown") {
		t.Fatalf("bad copy is %q, want unknown", badVerdict)
	}

	if g, b := decisionOf(t, good), decisionOf(t, bad); g != b {
		t.Fatalf("the declaration changed the decision: %+v vs %+v", g, b)
	}
}

type decision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

func decisionOf(t *testing.T, root string) decision {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".ckpt-governor", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d decision
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func verdictOf(t *testing.T, root string) string {
	t.Helper()
	var out bytes.Buffer
	runResumable([]string{"--dir=" + root}, &out)
	return strings.TrimSpace(out.String())
}

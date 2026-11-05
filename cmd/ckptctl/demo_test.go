package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The demo's whole point is that the four verdicts are the real ones: a missing shard must read as
// not resumable and broken, and the repair must produce a copy that reads as both resumable and
// verified.
func TestDemoKillThenRepair(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if code := runDemo([]string{"--dir", dir, "--scenario", "kill"}, &out); code != 0 {
		t.Fatalf("demo = %d: %s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"fault: rank1.pt deleted",
		"not resumable: rank 1 shard missing",
		"verify      broken",
		"1 problem",
		"next        ckptctl demo --repair",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("demo is missing %q:\n%s", want, text)
		}
	}

	var repaired strings.Builder
	if code := runDemo([]string{"--dir", dir, "--repair"}, &repaired); code != 0 {
		t.Fatalf("repair = %d: %s", code, repaired.String())
	}
	text = repaired.String()
	for _, want := range []string{
		"resumable (gen 1, step 300, world 2/2",
		"verify      verified",
		"0 problems",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("repair is missing %q:\n%s", want, text)
		}
	}
}

func TestDemoTornAndShortAreDifferentProblems(t *testing.T) {
	cases := map[string]string{
		"torn":  "tail hashes to",
		"short": "bytes, sealed at",
	}
	for scenario, want := range cases {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			var out strings.Builder
			if code := runDemo([]string{"--dir", dir, "--scenario", scenario}, &out); code != 0 {
				t.Fatalf("demo = %d: %s", code, out.String())
			}
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%s must say %q:\n%s", scenario, want, out.String())
			}
			// A torn or short shard is still present, so the declaration is resumable; the content
			// check is what refuses it. Both answers are true at once, which is the distinction the
			// product is built on.
			if !strings.Contains(out.String(), "resumable (gen 1, step 300") {
				t.Fatalf("%s: presence and content are separate answers:\n%s", scenario, out.String())
			}
		})
	}
}

func TestDemoJSON(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if code := runDemo([]string{"--dir", dir, "--json"}, &out); code != 0 {
		t.Fatalf("demo = %d: %s", code, out.String())
	}
	var got struct {
		Schema   int    `json:"schema"`
		Command  string `json:"command"`
		Dir      string `json:"dir"`
		Scenario string `json:"scenario"`
		Resume   struct {
			State string `json:"state"`
		} `json:"resume"`
		Verify struct {
			State string `json:"state"`
		} `json:"verify"`
		Storage struct {
			Class string `json:"class"`
		} `json:"storage"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("demo --json is not JSON: %v\n%s", err, out.String())
	}
	if got.Schema != 1 || got.Command != "demo" || got.Dir != dir {
		t.Fatalf("envelope = %+v", got)
	}
	if got.Resume.State != "not resumable" || got.Verify.State != "broken" {
		t.Fatalf("verdicts = %+v", got)
	}
	if got.Storage.Class == "" {
		t.Fatal("the storage class must be measured, not omitted")
	}
}

// A demo that can overwrite a real checkpoint is not a demo. The marker is the only permission.
func TestDemoRefusesAForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "real-checkpoint.bin")
	if err := os.WriteFile(keep, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if code := runDemo([]string{"--dir", dir}, &out); code != 1 {
		t.Fatalf("demo = %d, want 1: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "refusing to write") {
		t.Fatalf("the refusal must name the reason:\n%s", out.String())
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "do not touch" {
		t.Fatalf("the foreign file was touched: %q %v", data, err)
	}

	var clean strings.Builder
	if code := runDemo([]string{"--dir", dir, "--clean"}, &clean); code != 1 {
		t.Fatalf("clean = %d, want 1", code)
	}
}

// The demo's text is a tutorial, so it is a golden file: a change in wording is a change in the
// first thing a user reads. The storage class is measured, so a runner that is not local skips the
// golden rather than pretending the volume is a disk.
func TestDemoGolden(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		repair bool
	}{
		{"kill", []string{"--scenario", "kill"}, false},
		{"torn", []string{"--scenario", "torn"}, false},
		{"short", []string{"--scenario", "short"}, false},
		{"clean", []string{"--scenario", "clean"}, false},
		{"repair", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.repair {
				if code := runDemo([]string{"--dir", dir, "--scenario", "kill"}, io.Discard); code != 0 {
					t.Fatalf("the setup run failed with %d", code)
				}
			}
			args := []string{"--dir", dir}
			if c.repair {
				args = append(args, "--repair")
			}
			args = append(args, c.args...)
			var out strings.Builder
			if code := runDemo(args, &out); code != 0 {
				t.Fatalf("demo = %d: %s", code, out.String())
			}
			got := strings.ReplaceAll(out.String(), dir, "<dir>")
			if !strings.Contains(got, "storage local") {
				t.Skipf("the runner's storage is not local; the golden assumes a disk: %s", got)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "demo", c.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("%s golden differs:\n--- got ---\n%s--- want ---\n%s", c.name, got, want)
			}
		})
	}
}

func TestDemoCleanRemovesOnlyItsOwnDirectory(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if code := runDemo([]string{"--dir", dir, "--scenario", "clean"}, &out); code != 0 {
		t.Fatalf("demo = %d: %s", code, out.String())
	}
	var cleaned strings.Builder
	if code := runDemo([]string{"--dir", dir, "--clean"}, &cleaned); code != 0 {
		t.Fatalf("clean = %d: %s", code, cleaned.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the demo directory still exists: %v", err)
	}
}

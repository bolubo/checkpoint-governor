package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWriteEmitsFiles(t *testing.T) {
	out := t.TempDir()
	dir := t.TempDir()
	var buf strings.Builder
	code := runSetup([]string{"--write", out, "--dir", dir, "--queue", "lab", "--world", "/world", "--cluster", "a"}, &buf)
	if code != 0 {
		t.Fatalf("setup = %d: %s", code, buf.String())
	}
	for _, name := range []string{"governed-job.yaml", "submit.sh", "agent-mcp.json", "README.md"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
	}
	if !strings.Contains(buf.String(), "written") {
		t.Fatalf("the command must name what it wrote:\n%s", buf.String())
	}
}

func TestSetupWriteNeedsADirectoryAndAQueue(t *testing.T) {
	var buf strings.Builder
	if code := runSetup([]string{"--write", t.TempDir(), "--dir", t.TempDir()}, &buf); code != 2 {
		t.Fatalf("setup = %d, want 2", code)
	}
}

func TestSetupCheckJson(t *testing.T) {
	// Without kubectl this is blocked, but the JSON must still be one object with reasons.
	var buf strings.Builder
	code := runSetup([]string{"--check", "--dir", t.TempDir(), "--queue", "lab", "--json"}, &buf)
	if code != 1 {
		t.Fatalf("setup = %d, want 1 (no cluster in the test): %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), `"schema"`) && !strings.Contains(buf.String(), `"ready"`) {
		t.Fatalf("the check must print a report:\n%s", buf.String())
	}
}

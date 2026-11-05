package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupLocalNeedsNoCluster(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ckpt")
	var out strings.Builder
	if code := runSetup([]string{"--local", "--dir", dir}, &out); code != 0 {
		t.Fatalf("setup = %d: %s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{"setup " + dir + ": local", "volume", "promise", "next        ckptctl init", "Governor"} {
		if !strings.Contains(text, want) {
			t.Fatalf("setup is missing %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("setup did not create the directory: %v", err)
	}
}

func TestSetupLocalNeedsADirectory(t *testing.T) {
	var out strings.Builder
	if code := runSetup([]string{"--local"}, &out); code != 2 {
		t.Fatalf("setup = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "--dir") {
		t.Fatalf("usage was not printed:\n%s", out.String())
	}
}

func TestSetupWithoutAModeIsRefused(t *testing.T) {
	var out strings.Builder
	if code := runSetup([]string{"--dir", t.TempDir()}, &out); code != 2 {
		t.Fatalf("setup = %d, want 2", code)
	}
}

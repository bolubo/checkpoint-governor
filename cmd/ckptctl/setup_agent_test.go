package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupAgentPrintsWithoutApplying(t *testing.T) {
	project := t.TempDir()
	var out strings.Builder
	if code := runSetup([]string{"--agent", "claude", "--project", project, "--root", project}, &out); code != 0 {
		t.Fatalf("setup = %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "mcpServers") || !strings.Contains(out.String(), "ckptctl") ||
		!strings.Contains(out.String(), "\"mcp\"") {
		t.Fatalf("the snippet must name the server and the command:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatal("without --apply nothing may be written")
	}
}

func TestSetupAgentClaudeApplyIsIdempotentAndBacksUp(t *testing.T) {
	project := t.TempDir()
	args := []string{"--agent", "claude", "--apply", "--project", project, "--root", project}

	var out strings.Builder
	if code := runSetup(args, &out); code != 0 {
		t.Fatalf("setup = %d: %s", code, out.String())
	}
	cfgPath := filepath.Join(project, ".mcp.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ckptctl") || !strings.Contains(string(raw), "mcp") {
		t.Fatalf(".mcp.json does not register the server: %s", raw)
	}
	skill := filepath.Join(project, ".claude", "skills", "checkpoint-governor", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("the skill was not installed: %v", err)
	}
	if _, err := os.Stat(cfgPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("the first write must not create a backup (nothing was there)")
	}

	// A second identical run must not rewrite anything, so it must not create a backup either.
	out.Reset()
	if code := runSetup(args, &out); code != 0 {
		t.Fatalf("second setup = %d: %s", code, out.String())
	}
	if _, err := os.Stat(cfgPath + ".bak"); !os.IsNotExist(err) {
		t.Fatal("an identical second run rewrote the file")
	}

	// A changed file is backed up before it is replaced.
	if err := os.WriteFile(cfgPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runSetup(args, &out); code != 0 {
		t.Fatalf("third setup = %d: %s", code, out.String())
	}
	if _, err := os.Stat(cfgPath + ".bak"); err != nil {
		t.Fatal("a replaced configuration must leave a backup")
	}
}

func TestSetupAgentCodexAppendsToHome(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	args := []string{"--agent", "codex", "--apply", "--project", project, "--root", project}

	var out strings.Builder
	if code := runSetup(args, &out); code != 0 {
		t.Fatalf("setup = %d: %s", code, out.String())
	}
	cfg, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "[mcp_servers.ckptctl]") {
		t.Fatalf("codex config does not carry the block: %s", cfg)
	}
	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "checkpoint-governor", "SKILL.md")); err != nil {
		t.Fatalf("the skill was not installed: %v", err)
	}

	out.Reset()
	if code := runSetup(args, &out); code != 0 {
		t.Fatalf("second setup = %d: %s", code, out.String())
	}
	again, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if string(again) != string(cfg) {
		t.Fatal("the second run appended the block again")
	}
}

func TestSetupAgentUnknown(t *testing.T) {
	var out strings.Builder
	if code := runSetup([]string{"--agent", "cursor"}, &out); code != 2 {
		t.Fatalf("setup = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "unknown agent") {
		t.Fatalf("the refusal must name the agent:\n%s", out.String())
	}
}

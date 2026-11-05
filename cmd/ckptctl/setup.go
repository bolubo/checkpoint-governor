package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bolubo/checkpoint-governor/internal/setup"
	"github.com/bolubo/checkpoint-governor/internal/storage"
)

// runSetup prepares a deployment. In this milestone it has one mode, --local, which needs no cluster
// and no kubectl: it makes the directory, measures what the volume promises, and prints the four
// lines of glue plus the next command. The cluster check/write modes are 0.28's later milestone and
// live in the same command so a user has one entry point.
func runSetup(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(out)
	local := fs.Bool("local", false, "prepare one checkpoint directory on this machine (no cluster)")
	dir := fs.String("dir", "", "the checkpoint directory")
	agent := fs.String("agent", "", "print (or with --apply, install) an MCP registration for claude|codex")
	apply := fs.Bool("apply", false, "with --agent, write the configuration and the skill (backup, idempotent)")
	project := fs.String("project", ".", "the project directory an agent configuration is written into")
	root := fs.String("root", "", "the checkpoint root the MCP server may read (default: --dir)")
	check := fs.Bool("check", false, "read the cluster and the volume, change nothing")
	write := fs.String("write", "", "write governed-job.yaml, submit.sh, agent-mcp.json and README.md here")
	queue := fs.String("queue", "", "the ClusterQueue the held arrival goes to")
	context := fs.String("context", "", "the kubectl context to read")
	image := fs.String("image", "", "the image a generated governed Job runs")
	claim := fs.String("claim", "", "the PVC a generated governed Job mounts")
	world := fs.String("world", "", "the observer's world directory, for submit.sh")
	cluster := fs.String("cluster", "", "the cluster name the observer writes, for submit.sh")
	asJSON := fs.Bool("json", false, "with --check, print the report as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(out, "usage: ckptctl setup --local --dir=<checkpointDir>")
		fmt.Fprintln(out, "       ckptctl setup --check --dir=<dir> --queue=<name> [--context=<ctx>] [--json]")
		fmt.Fprintln(out, "       ckptctl setup --write=<outDir> --dir=<dir> --queue=<name>")
		fmt.Fprintln(out, "       ckptctl setup --agent=claude|codex [--apply] [--project=<dir>] [--root=<dir>]")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *agent != "" {
		return setupAgent(*agent, *apply, *project, *root, *dir, out)
	}
	if *check {
		if *dir == "" {
			fs.Usage()
			return 2
		}
		rep := setup.Check(*dir, *context, *queue, setup.Exec{Context: *context})
		if *asJSON {
			raw, err := rep.Marshal()
			if err != nil {
				fmt.Fprintf(out, "setup: %v\n", err)
				return 1
			}
			fmt.Fprintf(out, "%s\n", raw)
		} else {
			fmt.Fprintf(out, "setup check %s", *dir)
			if *queue != "" {
				fmt.Fprintf(out, ": queue %s", *queue)
			}
			fmt.Fprintln(out)
			for _, row := range rep.Rows {
				fmt.Fprintf(out, "%-11s %-8s %s", row.Name, row.Status, row.Detail)
				if row.Remedy != "" {
					fmt.Fprintf(out, " · %s", row.Remedy)
				}
				fmt.Fprintln(out)
			}
			if rep.Ready {
				fmt.Fprintln(out, "\nsetup: ready")
			} else {
				fmt.Fprintln(out, "\nsetup: blocked")
			}
		}
		if rep.Ready {
			return 0
		}
		return 1
	}
	if *write != "" {
		if *dir == "" || *queue == "" {
			fs.Usage()
			return 2
		}
		files, err := setup.Write(*write, setup.WriteOptions{
			Dir: *dir, Root: *root, Image: *image, Claim: *claim,
			Queue: *queue, WorldDir: *world, Cluster: *cluster,
		})
		if err != nil {
			fmt.Fprintf(out, "setup: %v\n", err)
			return 1
		}
		for _, f := range files {
			fmt.Fprintf(out, "written     %s\n", f)
		}
		return 0
	}
	if !*local || *dir == "" {
		fs.Usage()
		return 2
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(out, "setup: cannot create %s: %v\n", *dir, err)
		return 1
	}
	r, err := storage.Probe(*dir)
	if err != nil {
		fmt.Fprintf(out, "setup: blocked — %v\n", err)
		return 1
	}

	fmt.Fprintf(out, "setup %s: local\n", *dir)
	fmt.Fprintf(out, "volume      %s (%s), promise %s\n", r.Class, r.FSName, r.Promise)
	if !r.Safe() {
		fmt.Fprintf(out, "warning     this volume promises nothing beyond what is readable now; do not resume across pods from it\n")
	}
	fmt.Fprintf(out, "next        ckptctl init --dir=%s --duration=4h\n", *dir)
	fmt.Fprintf(out, "loop        g = Governor(%q, start_engine=True)\n", *dir)
	fmt.Fprintln(out, "            for step in ...: g.step(step)")
	fmt.Fprintln(out, "            if g.requested(): save(); g.sealed(gen, step, shards)")
	fmt.Fprintln(out, "            g.ending(); g.close()")
	return 0
}

const skillText = `# checkpoint-governor

Use when a training run's checkpoint cannot resume, when status looks wrong, or when a volume's
durability is in question.

## Steps

1. Call the evidence tool for the directory. It carries status, resume, verify, attempts, storage and
   problems in one bundle.
2. If resume.state is "not resumable": report resume.line, resume.missingRank and the first
   problems[].code. Do not guess a different reason.
3. If newest.verified is false: report the shard name and whether it is missing, short, or has a
   different tail hash.
4. If storage.class is "shared-unsafe": say the copy promises nothing across pods or restarts.

## Rules

- Read-only: never edit a checkpoint, a manifest, or a plan.
- If a field is "unknown", say unknown and name the reason; never fill it in.
- Prefer the evidence bundle over prose you remember from a different version.
`

func setupAgent(agent string, apply bool, project, root, dir string, out io.Writer) int {
	if agent != "claude" && agent != "codex" {
		fmt.Fprintf(out, "setup: unknown agent %q (want claude or codex)\n", agent)
		return 2
	}
	if root == "" {
		root = dir
	}
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(out, "setup: %v\n", err)
		return 1
	}
	absProject, err := filepath.Abs(project)
	if err != nil {
		fmt.Fprintf(out, "setup: %v\n", err)
		return 1
	}

	cfg := map[string]any{
		"mcpServers": map[string]any{
			"ckptctl": map[string]any{
				"type": "stdio", "command": "ckptctl",
				"args": []string{"mcp", "--root", absRoot},
			},
		},
	}
	jsonSnippet, _ := json.MarshalIndent(cfg, "", "  ")
	tomlSnippet := fmt.Sprintf("[mcp_servers.ckptctl]\ncommand = \"ckptctl\"\nargs = [\"mcp\", \"--root\", %q]\n", absRoot)

	fmt.Fprintf(out, "setup agent %s: project %s\n", agent, absProject)
	if !apply {
		if agent == "claude" {
			fmt.Fprintf(out, "mcp         %s\n", jsonSnippet)
			fmt.Fprintln(out, "note        run with --apply to write .mcp.json and the skill")
		} else {
			fmt.Fprintf(out, "mcp         %s", tomlSnippet)
			fmt.Fprintln(out, "note        run with --apply to append this to ~/.codex/config.toml")
		}
		return 0
	}

	switch agent {
	case "claude":
		if err := writeMCPJSON(filepath.Join(absProject, ".mcp.json"), cfg); err != nil {
			fmt.Fprintf(out, "setup: %v\n", err)
			return 1
		}
		skill := filepath.Join(absProject, ".claude", "skills", "checkpoint-governor", "SKILL.md")
		if err := writeWithBackup(skill, []byte(skillText)); err != nil {
			fmt.Fprintf(out, "setup: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "mcp         %s (written)\n", filepath.Join(absProject, ".mcp.json"))
		fmt.Fprintf(out, "skill       %s (written)\n", skill)
		fmt.Fprintln(out, "note        restart Claude Code; the project-scoped server is read-only")
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(out, "setup: %v\n", err)
			return 1
		}
		cfgPath := filepath.Join(home, ".codex", "config.toml")
		if err := appendCodex(cfgPath, tomlSnippet); err != nil {
			fmt.Fprintf(out, "setup: %v\n", err)
			return 1
		}
		skill := filepath.Join(absProject, ".agents", "skills", "checkpoint-governor", "SKILL.md")
		if err := writeWithBackup(skill, []byte(skillText)); err != nil {
			fmt.Fprintf(out, "setup: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "mcp         %s (written)\n", cfgPath)
		fmt.Fprintf(out, "skill       %s (written)\n", skill)
		fmt.Fprintln(out, "note        restart Codex; if the block was already present, edit the root by hand")
	}
	return 0
}

func writeMCPJSON(path string, cfg map[string]any) error {
	existing := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("%s is not JSON: %w", path, err)
		}
	}
	servers, _ := existing["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers["ckptctl"] = cfg["mcpServers"].(map[string]any)["ckptctl"]
	existing["mcpServers"] = servers
	raw, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return writeWithBackup(path, append(raw, '\n'))
}

func appendCodex(path, block string) error {
	existing := ""
	if raw, err := os.ReadFile(path); err == nil {
		existing = string(raw)
	}
	if existing != "" && containsBlock(existing) {
		return nil
	}
	if existing != "" && !endsWithNewline(existing) {
		existing += "\n"
	}
	existing += "\n" + block
	return writeWithBackup(path, []byte(existing))
}

func containsBlock(s string) bool { return strings.Contains(s, "[mcp_servers.ckptctl]") }

func endsWithNewline(s string) bool {
	return len(s) > 0 && s[len(s)-1] == '\n'
}

func writeWithBackup(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil {
		if string(old) == string(data) {
			return nil
		}
		if err := os.WriteFile(path+".bak", old, 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

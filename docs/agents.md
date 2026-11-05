# Ask your agent

REA-style distribution, for a checkpoint: the facts an agent needs are one read-only MCP server away,
and installing it is one command.

```bash
ckptctl setup --agent=claude --apply --project=. --root=/ckpt
ckptctl setup --agent=codex  --apply --project=. --root=/ckpt
```

Without `--apply` the command prints the registration snippet and writes nothing. With `--apply` it
writes the configuration and the skill, makes a backup of every file it replaces, and is idempotent:
a second identical run changes nothing.

| agent | config | skill |
|---|---|---|
| Claude Code | `<project>/.mcp.json` | `<project>/.claude/skills/checkpoint-governor/SKILL.md` |
| Codex | `~/.codex/config.toml` (appended) | `<project>/.agents/skills/checkpoint-governor/SKILL.md` |

Restart the agent after setup. If a configuration file already carries a `ckptctl` entry, the command
leaves it alone; edit the root by hand if it changed.

## The tools

The server is `ckptctl mcp --root=<dir>`; it speaks JSON-RPC over stdio and has no write tools.

| tool | question |
|---|---|
| `status` | what is the directory doing now |
| `resumable` | can it resume, and if not, which rank or state is missing |
| `verify` | is the newest copy whole (one generation, or all) |
| `explain` | why the last decision was what it was |
| `storage` | what this volume promises: `local`, `shared-safe`, `shared-unsafe` |
| `evidence` | the whole schema-1 bundle |

Every result carries the same fields as the corresponding `--json` command, plus `structuredContent`
for clients that read it directly.

## Safety

- **Read-only.** The server never writes a checkpoint, a manifest, a plan, or the world.
- **Bounded.** A tool may read only under the `--root` directories; `..`, absolute paths outside them,
  and symlinks that escape them are refused with the reason.
- **No process control.** It starts no analysis engine, no training job, and no gate.
- **No network.** It reads local files only.

## Two questions it can answer

- "This run died and did not resume. Why?" → `resumable` names the missing rank; `evidence` carries
  the shard that is gone and the remedy.
- "Can I trust this volume after the node dies?" → `storage` returns `shared-safe` only when the
  measurement passed; `shared-unsafe` says not to resume across pods from it.

## What it does not claim

- The skill is instructions, not a second implementation: every verdict comes from the engine.
- The server is a reader; the gate, the loop and the writer remain the only parties that act.

# Evidence: one bundle, one answer

```bash
ckptctl evidence --dir=/ckpt/job --json
```

`evidence` assembles what `status`, `resumable`, `verify` and `doctor --storage` already decided. It
does not re-judge: a bundle that could disagree with those commands would be a second answer to one
question, and the product is built on there being one.

The same JSON is what `doctor`, `verify`, `explain` and `audit` print with `--json`, and what the
MCP tools return. Schema 1 is frozen for this release: fields may be added, never removed or renamed.

## The bundle

```json
{
  "schema": 1,
  "generatedAt": "2026-11-05T…Z",
  "dir": "/ckpt/job",
  "unitId": "team/job",
  "status": {
    "generation": 7, "step": 300, "phase": "idle",
    "sealedAt": "2026-11-05T…Z", "writing": false
  },
  "resume": {
    "state": "not resumable", "reason": "shard-missing",
    "line": "not resumable: rank 1 shard missing",
    "generation": 7, "step": 300,
    "world": {"ranks": 2, "tp": 1, "pp": 1, "dp": 1, "reshardable": false},
    "missingRank": 1
  },
  "newest": {
    "generation": 7, "step": 300, "sealedAt": "…",
    "state": "broken", "reason": "short", "detail": "shard rank1.pt is missing",
    "verified": false,
    "shards": [
      {"name": "rank0.pt", "member": 0, "size": 2048, "present": true, "tailOk": true},
      {"name": "rank1.pt", "member": 1, "size": 2048, "present": false, "tailOk": false}
    ]
  },
  "attempts": [{"id": "…", "trigger": "risk.preempt", "outcome": "sealed", "startedAt": "…"}],
  "storage": {"class": "local", "promise": "process death: …", "fs": "ext4"},
  "problems": [
    {"code": "shard_missing", "detail": "not resumable: rank 1 shard missing",
     "remedy": "rewrite the shard and seal the generation again"}
  ]
}
```

Every field is optional except `schema`, `generatedAt`, `dir`, `status`, `resume` and `problems`; a
directory that has not been governed yet still produces a bundle (`status.phase` is empty, `resume.state`
is `unknown`).

## Problem codes

`problems[].code` is a closed set. New codes are additions, so a reader must ignore one it does not
know rather than fail the bundle.

| code | what happened | remedy |
|---|---|---|
| `shard_missing` | a shard the manifest names is not on the volume | rewrite it and seal again |
| `shard_short` | a shard is smaller than the manifest says | rewrite it and seal again |
| `tail_mismatch` | the declared tail hash does not match | rewrite it and seal again |
| `manifest_invalid` | the manifest cannot be parsed or believed | the writer must seal a valid manifest |
| `declaration_invalid` | `resume.json` is missing or lacks a required state | write a declaration that says what a restore needs |
| `world_mismatch` | the restore's world differs and the format cannot re-shard | resume under the world it was written with |
| `storage_unsafe` | `doctor --storage` says the volume promises nothing across pods | use a shared volume, or do not resume across pods |
| `unsealed` | a generation directory exists without a manifest | the write is in flight; wait or rewrite |

## One answer, checked

- `evidence.resume` is the same verdict `resumable --json` prints; a test compares the reason,
  generation and step.
- `evidence.status` is the ledger `status --json` prints.
- `evidence.newest` is `verify --json` for the same generation.
- `evidence.storage` is the report `doctor --storage` prints.

The command is read-only over the checkpoint data. The storage probe writes one temporary file inside
its own `.ckptctl-storage-*` directory and removes it; it never touches a shard or a manifest.

## Exit codes

`0` when the directory can be read (problems are reported, not returned), `1` when it cannot, `2` for
a usage error. A script that wants a gate uses `resumable` or `verify`: they carry the product's
three meanings (yes / no / cannot decide).

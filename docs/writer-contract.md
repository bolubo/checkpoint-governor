# What a checkpoint writer must do

One page for whoever owns the checkpoint library. The governor does not write your checkpoints; it reads
what you wrote, tells you whether it is whole, and asks again when it is not (0025). For that to mean
anything, three things have to be true.

## 1. Save on demand

A request is one small file. Write it atomically and read it each save interval:

```json
{"generation": 7, "reason": "risk.preempt", "notAfter": "2026-11-02T03:20:10Z", "full": true}
```

Poll at a cadence short enough that the deadline means something. A writer that only saves on a ten-minute
timer cannot be protected by a request it reads in nine — and the governor will report `requested` with no
`verified`, which is the honest state rather than a failure.

## 2. Seal last

```
<gen>/rank-00.bin          write
<gen>/rank-01.bin          write
<gen>/manifest.json.tmp    write the manifest
<gen>/manifest.json        rename into place  ← only now does the generation exist
```

A manifest that exists therefore means every shard was already written, and a generation directory with no
manifest is a **write in flight** — the governor waits for it instead of asking again. That single ordering
is what makes the whole verification cheap.

## 3. One generation per request

Write into a new generation directory each time; never rewrite the one you already sealed. The number is
what lets a late write be told apart from an answer: a copy of generation 7 finishing after the governor
asked for 8 is `stale`, and it will not be accepted however whole it is.

## The manifest

```json
{
  "generation": 7,
  "step": 48210,
  "sealedAt": "2026-11-02T03:20:07Z",
  "writer": "torch-dcp/2.5",
  "shards": [
    {"name": "rank-00.bin", "size": 52428800,
     "sha256": "…", "tailSha256": "…"}
  ]
}
```

- **`name` is relative to the generation directory.** Absolute paths and `..` are refused, and so is a name
  that is not in canonical form.
- **`sha256`** covers the whole shard.
- **`tailSha256`** covers the last `min(size, 1 MiB)` bytes. Without it the governor cannot verify cheaply:
  it must either read every byte or fall back to a size check, and it will report `broken/hash` rather than
  claim a verification it did not do.
- **`sealedAt`** is used to order generations, never to decide whether a deadline passed — that is the
  governor's clock, because this field is a claim by the party being checked.

## What the governor does with it

| what you left | what it says | what happens |
|---|---|---|
| manifest + all shards, hashes agree | `verified` | done |
| manifest + all shards, no content check yet | `sealed` | it reads, then decides |
| a shard missing or short | `broken/short` | asks for generation 7+1 |
| shard content differs | `broken/hash` | asks for a new generation, **from scratch** |
| generation older than the request | `broken/stale` | waits for the one it asked for |
| directory with no manifest | `unverified/waiting` | waits until the deadline |
| no manifest past the deadline | `broken/expired` | asks again, and records your lateness |
| manifest unparseable or hostile | `broken/invalid` | asks once, then reports |
| the request cannot fit the queue's quota | `broken` | **stops**, reports `cannot-fit` |

Two things it never does: it never writes into your directory, and it never deletes a generation. Retention
arrives as a recommendation — "these may go" — and you execute it.

## What this does not promise

That the checkpoint can be resumed. `verified` means the bytes match the manifest; the restorable check —
load it and continue one step — is the ML stage's work, and until then the receipt says `verified`, not
`safe`.

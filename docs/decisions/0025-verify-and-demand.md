# 0025 — We verify the checkpoint and demand another; we never write it

Date: 2026-11-02. Status: accepted. Follows 0015 (the risk core) and 0017 (two numbers).

## Context

The product decides when a checkpoint is worth writing. It has never written one, and the temptation
arrives with ML workloads: a customer's checkpoint is large, their library is slow, and "just write it
ourselves" looks like a feature.

The X-layer rig says why that temptation is a trap. Under a kill during a write, the governed arm ends
with one usable checkpoint and the naive arm with none — **and both lose a second of work**. "We kept
something" is true; "we eliminated the loss" is not. Only verification can tell those two apart in a
customer's deployment, and only the owner of the data can be responsible for the bytes.

## Decision

**We never write, repair, or delete a checkpoint.** We read what the writer left, say what it is, and when
it is wrong we demand another one.

Three states, and no fourth:

| state | evidence | who can check it |
|---|---|---|
| requested | we decided and asked | us, from the plan |
| sealed | every shard arrived and the manifest was written last | us, from the files |
| verified | the content reads back consistent with the manifest | us, by reading it |

We never claim **durable** in the storage sense. That belongs to the volume.

The contract with the writer has three requirements, and they are the whole of the integration:

1. **save on demand** — the request is a small file, written atomically, carrying a generation and a
   deadline; a workload that only checkpoints on a timer cannot be protected by it;
2. **seal last** — the manifest is written after every shard, to a temporary name, then renamed. A manifest
   that exists therefore means every shard it names was written, and a generation directory *without* one is
   a write in flight;
3. **one generation per request** — a rewrite is a new generation, never a retry of the old one, so a slow
   writer finishing 7 late cannot satisfy a request for 8.

## Consequences

- **The claim is narrower and checkable.** "We asked, we checked, we said which of the three it is" is a
  sentence a receipt can support. "Your work is on disk" is not.
- **The verification has to be affordable**, so it is tiered: structure (milliseconds), tail sample
  (seconds), full hash (minutes, on suspicion), restore (a drill, and deferred to the ML stage because it
  needs a loader). A writer that declares no tail hash gets `broken/hash` from the cheap path rather than a
  pass it did not earn.
- **A rewrite is bounded.** Attempts and elapsed time both have a budget, and `cannot-fit` is a report
  rather than a retry: rewriting into a full disk turns one bad checkpoint into a busy volume.
- **Retention stays the owner's.** We recommend "delete up to generation N"; we never delete.
- **A checkpoint's manifest is untrusted input.** It arrives on the customer's volume, written by their
  library: shard names are paths, sizes are claims, `sealedAt` is a claim, and all three are checked rather
  than believed.

## What this does not promise

That the work can be resumed. `verified` means the bytes match the manifest, and the restorable check —
load it and continue one step — is the only thing that proves otherwise. It needs a loader we can drive,
which is why it belongs to the ML stage.

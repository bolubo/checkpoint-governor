# Verify, and demand again

Status: design. Follows 0015 (the risk core), 0017 (two numbers) and `policy.md`'s copy states.

The product does not write checkpoints. That was settled early and it stays settled: the data path belongs
to the workload, and a controller that owns the bytes also owns every way they can be lost. What this
document designs is the thing that replaces writing — **we verify what landed, and when it is wrong we
demand another one.**

A worked example of why this matters: the X-layer rig kills a job mid-write. The governed arm ends with
one usable checkpoint and the naive arm with none, but **both lose a second of work**
(`experiments/rig`, `kill-during-write`). "We kept something" is true. "We eliminated the loss" is not,
and only verification can tell the difference between the two in a customer's deployment.

## The claim, split in three

Today we can make the first one. The second and third are what this design adds.

| claim | means | who can check it |
|---|---|---|
| **requested** | we decided a write was needed and asked for it | us, from the plan |
| **sealed** | every part arrived and the manifest was written last | us, from the files |
| **verified** | the sealed copy reads back consistent | us, by reading it |

We never claim **durable** in the storage sense — `fsync` semantics, replication, the volume's own word.
That belongs to the volume and the customer's storage class, and pretending otherwise is how a controller
ends up blamed for a disk.

## Roles

| role | owns | never does |
|---|---|---|
| **writer** (the workload's checkpoint library) | writing bytes, sealing, the manifest | deciding when |
| **verifier** (this product) | reading, checking, demanding a rewrite, keeping the receipt | writing, repairing, deleting |
| **owner** (the operator) | the volume, retention, deletion | — |

The writer is replaceable. Anything that can save on demand and seal last satisfies the contract, which
is the point: we are not betting on one library.

## The contract

### 1. Save on demand, not only on a timer

The product asks; the writer must be able to answer. A workload that only checkpoints every ten minutes
cannot be protected by a request it will read in nine.

```
<dir>/.ckpt/request.json      we write this (small, atomic, one file)
<dir>/.ckpt/manifest.json     the writer writes this, last, per generation
<dir>/gen-0007/...            the writer's own layout, opaque to us
```

The request carries a **generation** and a deadline:

```json
{"generation": 7, "reason": "risk.preempt", "notAfter": "2026-11-02T03:20:10Z", "full": true}
```

It is written to a temporary name and renamed — a half-written request is a request that was never made.

### 2. Seal last

The manifest is written **after** every shard, to a temporary name, then renamed:

```json
{"generation": 7, "step": 48210, "sealedAt": "2026-11-02T03:20:07Z",
 "shards": [{"name": "rank-00.bin", "size": 52428800,
             "sha256": "...", "tailSha256": "..."}],
 "writer": "torch-dcp/2.5"}
```

Shard names are **relative to the generation directory**: a manifest that named its own parent would break
the moment the directory was renamed, and it is the parent that makes `..` exploitable.

`tailSha256` is the hash of the last **min(size, 1 MiB)** bytes, and it exists for one reason: the cheap
check needs something to compare against. Without it there are two options, both bad — read every byte on
every save, or call a file verified because it is the right size. A writer that declares no tail hash gets
`broken/hash` from the cheap path and `verified` only from a full read.

Two properties come from the ordering alone:

- **a manifest that exists means every shard was already written** — that is what "sealed" buys, and it is
  the only reason a rename of one small file is worth a protocol;
- **a generation directory with no manifest is a write in flight**, not a broken copy. The ledger already
  records this as `AbsentAt` (`internal/advise`), and the distinction matters: one is answered by waiting,
  the other by rewriting.

### 3. One generation, one identity

A rewrite is a new generation, never a retry of the old one. This is what keeps a late writer from
passing a stale copy off as the answer to a newer request:

| event | generation |
|---|---|
| the request | 7 |
| the write lands, sealed | 7 |
| verification says `broken` | rewrite asked for **8** |
| a slow writer finishes 7 anyway | 7 is superseded; it can never satisfy the request for 8 |

## What verification checks, and what it costs

Hashing a 100 GB checkpoint on every save is not a design, it is a way to guarantee nobody enables the
feature. Four levels, each with the job it is actually good at:

| level | checks | cost | when |
|---|---|---|---|
| **structure** | manifest parses; every listed shard exists; sizes match; generation is the requested one | milliseconds | every save |
| **sample** | hash of the **last** and first few MB of each shard | seconds | every save |
| **full** | hash every byte against the manifest | minutes | on suspicion, or before a restore drill |
| **restorable** | load it and continue one step | very expensive | periodic drill, not per save |

**Sample leans on where kills land.** A process killed mid-write leaves a short or torn *tail*; the
middle of a shard is written minutes earlier and is almost never what a kill damages. Checking the tail
catches the failure that actually happens, for seconds rather than minutes.

**Restorable is the only check that proves resumability**, and it is the one we cannot run on every save.
That asymmetry has to be stated in the receipt rather than smoothed over: `verified` means "the bytes
match the manifest", not "your job can resume".

## Why a copy is the way it is

Every state has exactly one action, and the action depends on the reason — not on how many times we have
tried:

| state | evidence | action |
|---|---|---|
| `waiting` | directory exists, no manifest, inside the deadline | wait |
| `sealed` | manifest present and parses | verify |
| `verified` | sample (or full) hash matched | record, done |
| `broken-short` | a shard is missing or smaller than declared | rewrite: generation + 1 |
| `broken-hash` | hash mismatch | rewrite **from scratch**: patching in place keeps the tear |
| `broken-stale` | manifest's generation is older than requested | ignore it; the request stands |
| `unsealed-expired` | no manifest, past the deadline | rewrite, and record the writer's lateness |
| `cannot-fit` | the volume cannot hold another generation | **stop rewriting**; report |

Three rules that keep this from turning into a loop:

1. **A rewrite is bounded.** Attempts and elapsed time both have a budget; when it is spent the state is
   `given-up` and the receipt says so. A controller that retries forever turns one bad checkpoint into a
   busy volume.
2. **Never rewrite into a full disk.** `cannot-fit` is a report, not a retry — the write that would make
   room is the customer's decision, not ours.
3. **Backoff is a consequence, not a policy lever.** It exists because writes cost; it is not a hint that
   a retry will work.

## The manifest is untrusted input

It arrives on the customer's volume, written by their library, read by us. That makes it a parser fed by
untrusted data, and the design has to say so:

- **shard names are paths**: reject absolute paths, `..`, and anything outside the generation directory;
- **sizes are claims**: refuse a manifest that declares more than the volume holds, and cap the shard count;
- **`sealedAt` is a claim too**: use it to order generations, never to decide whether a deadline passed —
  our own clock does that;
- **two writers, one directory**: the contract says one write per volume at a time (`volume-busy` already
  exists as a door); a manifest that changes under us during verification is treated as `unsealed`, not as
  a failure of the writer.

## What the receipt says

One line per generation, and the numbers that make the claim checkable later:

```
generation 7  requested 03:20:00  sealed 03:20:07  verified 03:20:09  sample  steps 48210  bytes 1.2G
generation 8  requested 03:20:12  sealed 03:20:19  verified 03:20:21  sample  steps 48210  bytes 1.2G
             attempt 1 of 2: broken-short (rank-03 truncated to 41MB of 50MB)
```

The aggregate that decides whether this product is worth running:

- **P(verified | a unit was taken)** — the number the whole design exists to raise;
- **attempts per preemption** — the cost, in writes, of raising it;
- **work lost, in seconds** — reported as a range, not a promise, because it depends on where the kill
  lands and no controller can move that.

## Testing

Verification is only worth what its fault coverage is worth. The matrix, all against real files:

| fault | what it must produce |
|---|---|
| kill mid-shard | `broken-short`, then a rewrite that verifies |
| torn tail (right size, wrong bytes) | `broken-hash`, rewrite from scratch |
| disk full during a write | `cannot-fit`, no retry storm |
| slow `fsync` | the deadline, not a hang |
| volume unmounted mid-write | `waiting` until the deadline, then `unsealed-expired` |
| two writers, same directory | one verified generation, no `broken` |
| manifest with `../` in a shard name | refused, loudly |
| restore drill | the loaded copy continues from the manifest's step |

The one that matters most is the first: **a bad checkpoint is detected, refused, and replaced — and at no
point is it recorded as a success.** That is the whole product in one experiment.

## Non-goals

- **Not writing, repairing, or compacting.** We read and we demand.
- **Not deleting.** Retention is a recommendation (`delete up to generation N`), executed by the owner. The
  sentence in `policy.md` that says the implementation deletes sealed copies older than a step should be
  read as *recommends*, and should be reworded to say it.
- **Not knowing the format.** Shards are opaque; sizes and hashes are the whole of our understanding.
- **Not promising durability.** `verified` is about consistency with the manifest, and the page should say
  so wherever it is printed.

## Open questions

1. **Who runs the drill?** A restorable check per save is unaffordable, but "we never load it" means the
   one failure that matters is untested in production. A weekly drill on the newest generation, driven by
   the same request channel, is the cheapest honest answer.
2. **What if the writer cannot save on demand?** Then the contract is unmet, and the honest product is
   `requested` only — plus a measurement of how often the timer happened to fire in time.
3. **How long does a rewrite have?** The deadline comes from the announced eviction time (0019's bound),
   which is knowable in Kueue and not knowable on a plain cluster. Where it is unknown, the budget is a
   guess and the receipt should say so.

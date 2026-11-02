# Product v0.2 — the release that says whether the work can continue

Status: scope, 2026-11-02. v0.2 ships in two halves, and both are about the same question asked at
different times: **is the checkpoint worth anything after the machine is gone?**

| half | question | state |
|---|---|---|
| **2A, verify and demand** | are the bytes whole, and if not, did we ask for another generation? | on the trunk (0025) |
| **2B, the resume contract** | what would a restore get, did the job adopt it, and can we prove it? | this release (0026) |

The halves share one principle: we never write, repair or delete a checkpoint. We read what the writer
left, say what it is, and ask again when it is wrong.

## 1. What ships in 2B

- **A declaration beside the manifest.** `resume.json` (schema 1) says what was saved, under which world,
  in which format, and what the loader will skip. It is untrusted input: bounded, strict, one JSON value.
- **Three verdicts, one per question.** `ckptctl resumable` (can this continue?), `ckptctl resumed` (did
  the job adopt the newest seal it could see?), `ckptctl drill` (prove it with one real step).
- **A one-line adapter.** `contrib/ckptctl-pytorch`: `step()`, `requested()`, `sealed()`, `resumed()`,
  `ending()`; zero runtime dependencies; the shards stay with the user's library.
- **Two accounts of time-to-resume.** Restart belongs to the platform; load belongs to the job (`load
  seconds`); a drill owns both and prints the total.

## 2. Acceptance lines

Every line is a command and the string it must print.

| # | command | must print |
|---|---|---|
| 1 | `ckptctl resumable --dir=F` | `resumable (gen 512, step 512, world 8/8, full state)`, exit 0 |
| 2 | `ckptctl resumable --dir=W` | `not resumable: optimizer state absent`, exit 1 |
| 3 | `ckptctl resumable --dir=F --world=16` | `not resumable: world changed 8 → 16, and this format is not reshardable`, exit 1 |
| 4 | `ckptctl resumed --dir=C` | `not adopted: step 3, newest sealed 512`, exit 1 |
| 5 | `make drill` | `drill: 4/4 — restorable, wrong-step, mutation-refused, no-loader` |
| 6 | `make resume` | `resume: 6/6 — resumable, weights-only, world-changed, resharding, unstated, adoption` |
| 7 | `make py` | `OK` (13 tests, standard library only) |
| 8 | `make demo` | `resume: ok — step 512 → 513` in act 4 |
| 9 | `TestFrozenNames` | `frozen: 14 conditions, 9 event kinds, 6 metrics, 2 exit codes` — unchanged |

The nine lines are the release: `make ci` runs every one of them.

## 3. What this release refuses

- **Not a format parser.** Shards stay opaque; a format we cannot judge is `unknown`, never "probably
  fine".
- **Not a writer or a repairer.** The adapter writes the manifest and the declaration — metadata — and
  never a shard.
- **Not a decision input.** A checkpoint that cannot be resumed never changes when we write. A bad copy
  that suppressed a write would be a feedback loop with no number to show it.
- **Not a scheduler.** No CRD, no queue, no gang barrier: the platform restarts, we judge what came back.
- **Not tensor-level truth.** A drill proves one step was taken; whether the step was good is the
  training's business.

## 4. Deferred, on purpose

Tensor/file format awareness beyond the declaration, automatic re-sharding, CRIU or host-level
checkpointing, and a dashboard. The adapter covers PyTorch first; the contract is written so any library
that can save on demand and seal last satisfies it (`docs/writer-contract.md`).

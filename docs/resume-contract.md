# The resume contract

Status: design, 2026-11-02. Second half of v0.2; follows 0025 (verify and demand) and 0026 (the verdict).

We verify that a checkpoint is whole (0025). That is not the same as saying the work can continue: a
sealed, byte-perfect copy can be unresumable because the library saved only the weights, because the
world size changed under a format that cannot re-shard, or because its own author excluded the optimizer
on purpose. This page is the contract that makes those cases nameable.

Three questions, three answers, no overlap:

| question | command | what it reads |
|---|---|---|
| are the bytes whole? | `ckptctl verify` | the manifest and the shards |
| **can this be resumed?** | `ckptctl resumable` | the declaration, the manifest, the world |
| **did the job actually resume?** | `ckptctl resumed` | the declaration, the glue, the newest seal |
| **prove it** | `ckptctl drill` | a loader, one step, and the files before and after |

## 1. The declaration

The writer leaves one small file beside the manifest, inside the generation directory:

```json
{
  "schema": 1,
  "generation": 7,
  "step": 512,
  "format": "dcp",
  "world": {"ranks": 8, "tp": 1, "pp": 1, "dp": 8, "reshardable": false},
  "state": {"model": true, "optimizer": true, "scheduler": true,
            "rng": true, "dataloader": true, "scaler": false, "ema": false},
  "partial": ["dataloader"],
  "loader": "python resume.py --steps 1"
}
```

- **`state` is what was saved**, one boolean per entry. A `false` is not a defect; it is a fact a resume
  has to be judged against.
- **`partial` is what the loader will skip.** Megatron's `--no-load-optim --no-load-rng` and TorchTitan's
  `exclude_from_loading` are legitimate modes; a partial restore is a *fine-tune*, and the verdict says
  so rather than pretending it is a resume.
- **`world` is the topology the writer ran under, and whether it can be read under another.** `ranks` is
  required; `tp`/`pp`/`dp` may be 0, which means "not declared".
- **There is no `missing` field.** The list of what is missing is our conclusion, derived from `state`
  and the world, not the writer's claim. A file that says its own verdict has misunderstood the contract.
- **`loader` is a suggestion, never a command.** We do not execute it because a file wrote it (0026).

The file is untrusted input: it arrives on the customer's volume, written by a library we do not
control. Unknown fields are refused rather than ignored, the size is bounded, a name in `partial` must be
a state the file actually holds, and one JSON value per file. A declaration that cannot be believed is
`unknown`, never "probably fine".

## 2. The verdicts

### Can it be resumed

Required for a full restore: model, optimizer, scheduler, RNG and dataloader position. `scaler` is
required only for mixed precision, `ema` only when the run keeps one.

| evidence | verdict | exit |
|---|---|---|
| all present, same world | `resumable (gen 7, step 512, world 8/8, full state)` | 0 |
| all present, world changed, declared reshardable | `resumable with resharding (8 → 16 ranks)` | 0 |
| world changed, not reshardable | `not resumable: world changed 8 → 16, and this format is not reshardable` | 1 |
| a required state absent | `not resumable: optimizer state absent` | 1 |
| a required state skipped by the loader | `not resumable: declared partial (optimizer) — this is a fine-tune, not a resume` | 1 |
| sealed, no declaration | `unknown: sealed, but nobody said what it holds` | 2 |
| a declaration that does not parse, or a format we cannot judge | `unknown: …` | 2 |

Exit codes keep the meaning the rest of the product uses: **0 yes, 1 checked and no, 2 cannot decide**.
A new code would be a compatibility change, and "we do not know" is not a failure of the copy.

### Did it resume

Comparing what the job says it adopted with the newest sealed generation:

| evidence | verdict | exit |
|---|---|---|
| a seal exists, the job reported nothing | `not adopted: step 3, newest sealed 512` | 1 |
| adopted an older generation | `adopted gen 6 (step 448), newest sealed 7 (step 512): 64 steps on disk unused` | 1 |
| adopted the newest | `adopted gen 7 (step 512)` | 0 |
| nothing sealed | `nothing to adopt: no sealed generation` | 0 |

This is the sentence that separates a restart from a resume. The job reports what it loaded through three
glue files (`resumed-generation`, `resumed-step`, `load-seconds`); absent means it did not say, which is
exactly what a cold start looks like from outside.

### Prove it

`ckptctl drill --dir=<copy> --loader="<command>"` runs the loader for one step and compares the step it
reports with the sealed step. `restorable` is the only claim earned this way, and it is the only one the
receipt may print with the word.

- the loader gets `CKPT_DIR` and `CKPT_GEN`, prints `step=<n>` as its last line, and must not save;
- the command comes from the person running it, or from `--trust-declared-loader`, and then the receipt
  says `using a command declared by an unverified file`;
- the tree is fingerprinted before and after (paths, sizes, mtimes, tail hashes). A drill that changed
  the checkpoint is **refused**, not passed: changing the evidence is not a test of it;
- a loader that reports step 0 is `not restorable: the loader reported step 0 (declared 512)` — the
  machine-checkable form of "it resumed from scratch".

## 3. What the receipt may say

```
generation 512  sealed 03:20:07  verified 03:20:09  steps 512  world 8/8  resumable (full state)
resumed         03:24:11  adopted gen 512 (step 512)  load 9.1s
drill           03:31:02  restorable: step 512 → 513  0.9s
```

Every line carries its own evidence: `resumable` from the declaration, `adopted` from the job, `restorable`
from a drill. None of them is upgraded by the others.

## 4. Two accounts of time-to-resume

A resume takes as long as two different things take, and no party owns both:

| span | owned by | do we print it? |
|---|---|---|
| killed → process running (scheduling, image, NCCL init) | the platform (JobSet, Ray, the scheduler) | no — it is not ours |
| process running → loader returns | the job, via `load-seconds` | yes |
| loader returns → first step | the job; in a drill, us | yes, in a drill |

In production we print the load time and say the restart is the platform's. In a drill we own both ends,
so we print the total. Claiming the whole span would be the same lie as claiming `durable`.

## 5. What this does not promise

- **Not tensor-level correctness.** We judge a declaration and, in a drill, one step. A checkpoint whose
  weights are numerically wrong is `restorable` and still wrong; that is the training's own business.
- **Not a format parser.** We never read shard contents (0025). Formats are judged by declaration, and
  an invented format is `unknown`.
- **Not a writer.** We write the declaration when a writer asks — one metadata file, atomically — and
  never a shard. The bytes stay with whoever owns the data.
- **Not a decision input.** Resumability never reaches the policy: a broken copy must not suppress the
  write that would replace it (0026).
- **Not a retention policy.** We may recommend "delete up to generation N"; deleting is the owner's.

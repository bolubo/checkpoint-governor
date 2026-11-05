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
  "requires": ["model", "optimizer", "scheduler", "rng"],
  "target": ".",
  "caveats": ["data-position-not-saved"],
  "loader": "python resume.py --steps 1"
}
```

- **`state` is what was saved**, one boolean per entry. A `false` is not a defect; it is a fact a resume
  has to be judged against.
- **`requires` is what this job needs.** Absent means the full restore's five (model, optimizer,
  scheduler, RNG, dataloader). A framework checkpoint declares four, because a framework re-iterates
  the dataset instead of saving a data position. Judging it against a dataloader its writer never had
  would refuse every copy it ever wrote.
- **`target` is which artifact the declaration describes.** `"."` is the directory it sits in
  (Hugging Face's `checkpoint-N/`); a file name is a sibling artifact (Lightning's `*.ckpt`, a Composer
  episode). Absent means `"."`. A path is refused: the file describes one copy, not a search.
- **`caveats` are facts that do not change the verdict.** Today the closed set is one entry,
  `data-position-not-saved`: a restore replays or restarts the epoch.
- **`min_reader` is the build this file needs.** A lower build refuses with the upgrade named, rather
  than reading a newer file as an older one.
- **`ext` is the only extension point.** Readers ignore its contents and record that it was there;
  every other unknown field is still refused.
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

Required for a full restore: model, optimizer, scheduler, RNG and dataloader position — unless the
declaration says otherwise in `requires`. `scaler` is required only for mixed precision, `ema` only when
the run keeps one. Only the declared set can make a copy unresumable: a `false` outside it is a fact,
not a refusal.

| evidence | verdict | exit |
|---|---|---|
| all present, same world | `resumable (gen 7, step 512, world 8/8, full state)` | 0 |
| all **required** present, declaration names four | `resumable (step 1000, requires model, optimizer, scheduler, rng, caveat: the data position is not saved; a restore replays or restarts the epoch)` | 0 |
| all present, world changed, declared reshardable | `resumable with resharding (8 → 16 ranks)` | 0 |
| world changed, not reshardable | `not resumable: world changed 8 → 16, and this format is not reshardable` | 1 |
| a required state absent | `not resumable: optimizer state absent` | 1 |
| a required state skipped by the loader | `not resumable: declared partial (optimizer) — this is a fine-tune, not a resume` | 1 |
| sealed, no declaration, no probe | `unknown: no declaration beside gen-0512 (and no probe_cmd to ask)` | 2 |
| a directory with no completion mark | `unknown: checkpoint-1000 is not sealed` | 2 |
| a probe that failed or timed out | `unknown: probe_cmd failed: …` | 2 |
| nothing sealed yet | `unknown: nothing sealed yet` | 2 |
| a declaration that does not parse, or a format we cannot judge | `unknown: …` | 2 |

Exit codes keep the meaning the rest of the product uses: **0 yes, 1 checked and no, 2 cannot decide**.
A new code would be a compatibility change, and "we do not know" is not a failure of the copy.

### What the controller does about it

`run` reads the newest sealed copy's declaration once per step (the answer is cached in the ledger, so a
view never probes the disk). If a required state is absent, it asks for **one** write — the only write
that can add it — and says why in the plan. If the next copy is still missing the same state, it stops
asking and reports a state a person has to act on:

```
the newest copy is missing optimizer, and a rewrite did not add it; a person has to look
```

A rewrite is a new generation, never a retry of the old copy: the writer is the only one that can put
the missing state into it. Two writes is the budget; a writer that keeps producing the same incomplete
copy is a fact to report, not a loop to run.

### Did it resume

Comparing what the job says it adopted with the newest sealed generation:

| evidence | verdict | exit |
|---|---|---|
| a seal exists, the job reported nothing | `not adopted: step 3, newest sealed 512` | 1 |
| adopted an older generation | `adopted gen 480 (step 448), newest sealed 512 (step 512): 64 steps on disk unused` | 1 |
| adopted the newest | `adopted gen 512 (step 512)` | 0 |
| nothing sealed | `nothing to adopt: no sealed generation` | 0 |
| step matches, generation does not | `adopted step 512 from generation 480, newest sealed is 512` | 1 |
| adopted step is ahead of the newest seal | `unknown: adopted step 520 is ahead of the newest seal (512, step 512)` | 2 |
| the report is not a number | `unknown: resumed-step is not a number: "…"` | 2 |

The comparison uses the seal that **existed when the job started** (its `boot` stamp), not the newest one
now: a job that resumed at 448 and has since saved 512 adopted 448, and calling that a loss would blame
the job for doing exactly what it should. `sealedAt` orders generations and never decides lateness (0025).

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

### Which copy is judged

A job directory may hold copies from several writers. `resumable`, `status` and the receipt all ask
the same finder, so they cannot name different copies.

| layout | how it is recognised | sealed when |
|---|---|---|
| `gen-N/` (this product) | directory, `manifest.json` | the manifest exists |
| `step-N/` (the older writer) | directory, `manifest.json` | the manifest exists |
| `checkpoint-N/` (Hugging Face) | directory name | `trainer_state.json` exists, or a declaration does |
| `*.ckpt` (Lightning) | file suffix | the file is non-zero |
| a Composer episode | `<file>.resume.json` beside it | the declaration exists and the artifact does |
| a user layout | `layout.json`'s `glob` | a declaration, or a successful probe |

Preference is fixed: `gen-*` first, then the highest step; at the same step, `checkpoint-*` beats
`*.ckpt`, which beats a Composer episode. A directory without a completion mark and without a
declaration is not sealed — it is a write in flight, and it is reported as `unknown: … is not sealed`
rather than counted as the newest copy. Two layouts at the same step are resolved by the same rule,
and `status` adds `note: step N exists as A and B; judged …` so the decision is not silent.

When the finder cannot name the layout at all, the user makes it known with
`.ckpt-governor/layout.json`:

```json
{
  "glob": "weights-*",
  "step_regex": "weights-(\\d+)",
  "declaration": "resume.json",
  "probe_cmd": "python probe.py --dir {dir}"
}
```

`probe_cmd` is the last glue: it must print `{"state": {...}, "requires": [...]}`, it runs only because
the user wrote it here, and it has five seconds. A probe that fails or times out is `unknown: probe_cmd
failed: …` — never "not resumable".

## 3. What the receipt may say

```
generation 512  sealed 03:20:07  verified 03:20:09  steps 512  world 8/8  resumable (full state)
resumed         03:24:11  adopted gen 512 (step 512)  load 9.1s
drill           03:31:02  restorable: step 512 → 513  0.9s
```

Every line carries its own evidence: `resumable` from the declaration, `adopted` from the job, `restorable`
from a drill. None of them is upgraded by the others.

A framework copy has no generation number. The same line names its step instead, and the copy it
compares against is the newest one that existed before the job started:

```
resumed         03:24:11  adopted step 50, load 0.005s
```

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

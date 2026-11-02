# Execution checklist — resume (v0.2, second half)

Companion to `resume-contract.md`. Every line is a task with a **done when** — a command and the string it
must print — because "implement the verdict" is not a task, it is a wish.

The first half of v0.2 (`docs/checklist-verify.md`) is already on the trunk: we verify what landed and
demand another. This half answers the other question: what a restore would get, and whether a job took it.

---

## P0 — the declaration (2h)

- [x] **0.1 The schema, written down.**
  `schema`, `generation`, `step`, `format`, `world{ranks,tp,pp,dp,reshardable}`, `state{model,optimizer,
  scheduler,rng,dataloader,scaler,ema}`, `partial`, `loader`.
  **done when** `go test ./internal/resume -run TestTheSchemaRefusesWhatItCannotBelieve` passes, and each
  refusal names the offending field.

- [x] **0.2 Untrusted input.**
  Unknown fields refused (not ignored), one JSON value per file, 64 KiB bound, `partial` names only states
  the file holds, no control characters in `loader`.
  **done when** `TestUnknownFieldsAreRefusedRatherThanIgnored` names `unknown field "missing"`, and
  `TestAnOversizedDeclarationIsRefusedBeforeItIsAllocated` names the bound.

- [x] **0.3 Fixtures by the real writer.**
  `internal/resume/testdata/{full,weights-only,partial-optimizer,reshardable,unknown-format,no-resume,
  invalid}` — shards and manifest from `ckpttrainer --contract`, declaration from the reference writer.
  **done when** `sh hack/gen-resume-fixtures.sh` prints `resume fixtures: 7 directories`, and
  `TestEveryFixtureSaysWhatItIs` passes (including "a declaration that names a different step than the
  manifest fails").

- [x] **0.4 The pages.**
  `docs/resume-contract.md` (schema, verdicts, receipt, two accounts of time), `docs/decisions/0026…`,
  this checklist, and the `.ckpt/` path fix in `docs/verify-and-demand.md`.
  **done when** `go test ./cmd/ckptctl -run TestDocsHaveNoDeadLinks` passes with the new pages counted.

## P1 — can it be resumed (3h)

- [x] **1.1 The matrix.** `internal/resume/verdict.go`: required-full = model+optimizer+scheduler+rng+dataloader;
  `Reason` is a closed set; a verdict carries the evidence it came from.
  **done when** `TestVerdictMatrix` has one case per row of the contract's table — eight cases, each
  asserting the sentence *and* the exit code.

- [x] **1.2 `ckptctl resumable`.** Exit 0 yes / 1 checked-and-no / 2 cannot decide.
  **done when** the command prints (observed):
  ```
  resumable (gen 512, step 512, world 8/8, full state)                                   exit 0
  not resumable: optimizer state absent (also: scheduler, rng, dataloader)              exit 1
  not resumable: world changed 8 → 16, and this format is not reshardable               exit 1
  resumable with resharding (8 → 16 ranks)                                              exit 0
  unknown: sealed, but nobody said what it holds                                        exit 2
  ```

- [x] **1.3 `ckptctl resumed`.** Six sentences, computed from `resumed-*` and the seal that existed at
  boot. **done when** `TestAdoptionFourQuadrants` passes, including the two inconsistencies
  (`step matches, generation does not` and `ahead of the newest seal`).

- [x] **1.4 The guard.** `TestResumabilityNeverChangesADecision`: two copies of the same work, one
  `resumable`, one `unknown`, publish the same `action`/`reason`.
  **done when** the test fails if the declaration is read by the loop.

- [x] **1.5 `make resume`.** Six assertions in one command, ending in a tally.
  **done when** it prints `resume: 6/6 — resumable, weights-only, world-changed, resharding, unstated, adoption`
  and runs inside `make ci`, `make verify` and `hack/prepush.sh`.

## P2 — the one-line integration (4h)

- [ ] **2.1** `contrib/ckptctl-pytorch` with zero runtime dependencies (`pyproject.toml`).
  **done when** `python -m pytest contrib/ckptctl-pytorch/tests -q` passes without torch installed.
- [ ] **2.2** `Governor.step/requested/sealed/resumed/ending` plus SIGTERM glue, rank0-only, every write
  by rename. **done when** `TestTheCallbackWritesOnlyGlue` shows no shard was created by the callback.
- [ ] **2.3** One preemption cycle end to end without torch.
  **done when** the test prints `cycle: requested → sealed → resumed(512, 512)`.
- [ ] **2.4** An `examples/` loader for real runs (not in CI), and the README's three-line snippet matches
  it byte for byte.
- [ ] **2.5** Regenerate P0's fixtures through the callback.
  **done when** `make resume` still prints `6/6` and `git status` is clean.

## P3 — proof (3h)

- [ ] **3.1** Fingerprint: paths, sizes, mtimes, tail hashes.
  **done when** `TestDrillRefusesAMutatedCheckpoint` prints `drill refused: the checkpoint changed during the drill`, exit 2.
- [ ] **3.2** Runner: `CKPT_DIR`/`CKPT_GEN`, `--timeout`, truncated output, last line `step=<n>`.
  **done when** a timeout prints `drill failed: the loader did not finish within 2m`, exit 1.
- [ ] **3.3** `ckptctl drill`, refusing a declared loader unless `--trust-declared-loader`.
  **done when** `resume: ok — step 512 → 513; 0.9s` (exit 0) and a step-0 loader prints
  `not restorable: the loader reported step 0 (declared 512)` (exit 1).
- [ ] **3.4** A synthetic loader for CI (`experiments/rig/tools/drill-loader`), and `make drill`.
  **done when** it runs inside `ci`, `verify` and `prepush`.

## P4 — release (2h)

- [ ] **4.1** The third act of the demo: preempt → one write → restart → resumed → receipt.
  **done when** the demo ends with `resumed: adopted gen 512 (step 512)`.
- [ ] **4.2** `docs/product-v0.2.md`, `docs/install.md` (pip line), `docs/compatibility.md` (declaration
  schema 1, three new glue files, zero new frozen names), README, CHANGELOG.
- [ ] **4.3** Compatibility, mechanically: `TestFrozenNames` unchanged and still printing
  `frozen: 14 conditions, 9 event kinds, 6 metrics, 2 exit codes`.
- [ ] **4.4** `v0.2.0`: assets, `sha256sum -c`, and an install whose `ckptctl version` says `v0.2.0`.

## The eight acceptance lines

| # | command | must print |
|---|---|---|
| 1 | `ckptctl resumable --dir=F` | `resumable (gen 512, step 512, world 8/8, full state)`, exit 0 |
| 2 | `ckptctl resumable --dir=W` | `not resumable: optimizer state absent`, exit 1 |
| 3 | `ckptctl resumable --dir=F --world=16` | `not resumable: world changed 8 → 16, and this format is not reshardable`, exit 1 |
| 4 | `ckptctl resumed --dir=C` | `not adopted: step 3, newest sealed 512`, exit 1 |
| 5 | `make drill` | `resume: ok — step 512 → 513` |
| 6 | `make resume` | `resume: 6/6 — …` |
| 7 | `TestFrozenNames` | `frozen: 14 conditions, 9 event kinds, 6 metrics, 2 exit codes` |
| 8 | `make demo` | `resumed: adopted gen 512 (step 512)` |

## Not in this version

Tensor-level checks, format parsers, writing/repairing/deleting checkpoints, automatic re-sharding,
CRIU or host-level checkpointing, a scheduler or a CRD, new metrics or events, and letting resumability
change a decision.

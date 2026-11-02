# Execution plan — the platform turn

Companion to `docs/decisions/0027-the-platform-is-the-customer.md`. Every line is a task with a
**done when** — a command and the string it must print. Numbers in the tables below are placeholders
until P0 prints real ones; a placeholder that survives into a release note is a lie.

## P0 — the replacement experiment (the only thing that can justify the pitch)

The claim under test: **the same loss bound, fewer writes.** One synthetic job, two arms, no cluster:

- **arm A, the framework default**: save every 500 steps, independently of time and deadline.
- **arm B, ours**: the window and the conditions, with `no-us` set to the same 500-step policy, so the
  comparison is "who decides", not "how often".

Both arms run the same number of steps, the same write cost, and the same pre-emption at a fixed step.

- [ ] **0.1 The two arms, one script.** `experiments/replacement/` builds both from the existing rig,
      with the write cost and the pre-emption point as flags.
  **done when** `make replacement` prints two rows per arm: `writes`, `io seconds`, `steps`.
- [ ] **0.2 The three numbers.** The comparison line names them and refuses to print a ratio without a
      bound: `replacement: writes 40 → 12 · io 6.0m → 1.8m · loss 2h → 2h`.
  **done when** the same command prints that shape with real numbers.
- [ ] **0.3 The falsifier.** If writes do not fall by at least 30% at an equal bound, the cost argument
      is dead: print `replacement: rejected — writes 40 → 34 at bound 2h`, exit 1, and stop the plan.
  **done when** the script has that branch and a test that exercises it with a fixed write cost.

## P1 — one host, real code (framework shape)

Composer is the host because it already ships the hard half: `autoresume`, a `latest` pointer, and a
save loop we can replace. The integration is a `CheckpointSaver`-compatible callback that asks the
policy instead of counting epochs.

- [ ] **1.1 The callback.** `contrib/ckptctl-composer/`: no new dependency beyond the policy binary;
      `save_interval` becomes `policy.says()`; `num_checkpoints_to_keep` becomes the retention rule.
  **done when** `make composer` prints `composer: 2/2 — autoresume on, writes 40 → 12, latest resolves`.
- [ ] **1.2 The equal-bound check.** The same loss bound as Composer's `1ep` default, proven by the
      same synthetic pre-emption the framework's own autoresume test uses.
  **done when** the second line of `make composer` is `bound 2h → 2h`.

## P2 — install once (platform shape)

A platform operator installs the sidecar for every training job; the tenant changes nothing. The
integration is a manifest, so it can be rendered and asserted on a laptop.

- [ ] **2.1 The injection.** `deploy/platform/`: a patch that adds the sidecar and the plan volume to a
      `kubeflow/trainer` job template, plus the same for a `Kueue`-managed workload.
  **done when** `make platform` prints `platform: 2/2 — trainer sidecar, kueue sidecar, 0 job changes`
      (rendered, not applied).
- [ ] **2.2 The tenant's view.** The tenant's manifest is byte-identical before and after.
  **done when** `make platform` prints `tenant manifest unchanged`.
- [ ] **2.3 The real run (optional, cluster only).** One job, one pre-emption, one receipt.
  **done when** the receipt line says `save  risk.preempt` and the plan file is visible in the job.

## P3 — the certificate (what the platform shows its tenants)

One page per job, generated from what we already measure: the bound, the exclusions it depends on, the
writes it cost, and the drills that earned `restorable`.

- [ ] **3.1 `ckptctl certify`.** Prints one page (Markdown) and one JSON, from the ledger and the seals.
  **done when** `make certify` prints `certificate: 1/1 — bound 2h · 3 exclusions · 1 drill verified`.
- [ ] **3.2 The exclusions are named, not implied.** At least: pre-emption denser than the assumption,
      a write longer than the remaining time, a loader that changes the checkpoint, a format we cannot
      judge.
  **done when** `grep -c '^## ' certificate.md` counts one heading per exclusion.

## P4 — the operator page

- [ ] **4.1 One page for the installer.** `docs/install.md` gains an "install once for every job" path
      and the terms page link; the one-job path stays as the verification step.
  **done when** `make docs` passes and the page contains the sentence "the tenant changes nothing".

## Order and why

P0 before everything: it is the only evidence that can fail, and it is cheap. P1 before P2: a framework
that already has `autoresume` gives us real code with real tests and no cluster. P2 before P3 because a
certificate only means something once somebody else's job produced it.

## Not in this version

No adapter zoo, no conformance kit, no dashboard, no scheduler, no PyPI release: each of them buys
surface without buying evidence (0020, 0027).

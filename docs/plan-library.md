# Execution plan — the library direction

Companion to `docs/decisions/0028-the-direction-is-a-library.md`. Every line is a task with a **done
when** — a command and the string it must print. Placeholders are placeholders until a step prints real
numbers; a placeholder that survives into a release note is a lie.

**Language:** Go stays the engine. Python is the front door and the client; it ships the engine, it does
not re-implement it. Decision 0028, "two delivery shapes, one core".

## Preconditions (owner decisions, before P1.4)

- [ ] **PyPI**: the package name, and whether publishing uses trusted publishing. Without a name, the
      wheel is built and tested locally; `pip install` from a URL stays documented but unverified.
- [ ] **Wheels**: which platforms ship first. Linux x86_64 is the one CI can build and test; macOS and
      Windows are optional until a user asks.

## P0 — the contrast (the growth engine)

The value is silence, so the first artifact is the counterfactual: one command, one table, no GPU, no
cluster, under two minutes from a clone.

- [ ] **0.1 The three arms, on a virtual clock.** `experiments/contrast/` imports `internal/loop` (already
      exported) and drives it with an injected `Now`, so the run is deterministic and fast. Arm A
      simulates the framework default (`save every N steps`); arm B saves nothing; arm C is the real
      policy with nothing configured; arm C2 is the real policy with a bound. The writer costs
      `write-ms` of virtual time per save.
  **done when** `make contrast` prints four rows — `writes`, `io seconds`, `coverage` (announced stop,
      silent death), `lost steps at the worst point` — and a test asserts the table has four rows and
      every row names its coverage.
- [ ] **0.2 The table is the README's first screen.** `pip install` and three lines come first; the
      contrast command and its table come second; Docker and Kubernetes move below.
  **done when** `make docs` passes and the README's first three sections are install, contrast,
      deploy.
- [ ] **0.3 A one-liner for people without Go or Python.** The same command inside a container.
  **done when** `docker run …` prints the same four rows.

## P1 — the Python front door (engine bundled, not rewritten)

- [ ] **1.1 A wheel that contains the engine.** `hack/build-wheel.sh` cross-compiles `ckptctl` into
      `ckptctl_pytorch/_bin/`, then builds the wheel; the binary is package data, not a download.
  **done when** the script prints `wheel: 1 file, engine inside` and `unzip -l` lists the binary.
- [ ] **1.2 The client starts the engine and reads the plan.** `Governor(start_engine=True)` launches
      `ckptctl run` for the directory, then `should_save()` reads `.ckpt-governor/state.json`.
  **done when** a subprocess test in a clean virtualenv sees the plan appear and `should_save()`
      answers `save` exactly when the plan says so.
- [ ] **1.3 Degradation when the engine dies.** If the engine exits, the client falls back to "write at
      end, and on an announced stop", and prints the reason once.
  **done when** a test kills the engine mid-run and the loop still writes at the end.
- [ ] **1.4 The demo without Go.** `python -m ckptctl_pytorch.demo` prints the P0 table from the bundled
      engine.
  **done when** the demo runs in a clean virtualenv and prints four rows.
- [ ] **1.5 The install page.** `docs/install-python.md`: install, three lines, what should appear, what
      to do when it does not.
  **done when** `make docs` passes and the page is linked from the README.

## P2 — the audience: Hugging Face Trainer

The integration is `save_strategy="no"` plus a callback that calls `trainer.save_checkpoint()` when
`should_save()` says so. Two lines, not a patch to `Trainer`.

- [ ] **2.1 The callback, against a fake trainer.** CI has no torch, so the contract test drives a
      minimal object with `save_checkpoint()` and asserts the call sequence.
  **done when** `make py` prints `hf: 2/2 — default 2 saves, governor 1 save`.
- [ ] **2.2 The real run (manual, torch only).** `make hf` runs a 1000-step CPU loop with `save_steps=500`
      and then with the callback.
  **done when** it prints `hf: 40 writes → 1` with real numbers (or the scaled equivalent for the steps
      used), and `trainer.state` is unchanged.
- [ ] **2.3 The supported-integration page.** The exact diff, the receipt to expect, and the exclusions.
  **done when** `make docs` passes and the page is linked from the README.

## P3 — the second and third integrations

Same shape, same contract test; the real runs need torch, so they stay manual targets.

- [ ] **3.1 Lightning.** A callback that replaces `ModelCheckpoint`'s interval while keeping its
      `save_top_k` and `last` semantics.
  **done when** `make lightning` prints `lightning: 40 writes → 1, latest resolves`.
- [ ] **3.2 Composer.** Replace `save_interval='1ep'` and keep its `latest-rank{rank}` pointer.
  **done when** `make composer` prints `composer: 40 writes → 1, latest resolves`.

## P4 — the contributor ladder

- [ ] **4.1 The integration template.** One page: how to write a callback for framework X, what the
      receipt must contain, what the test must assert, which files to add.
  **done when** the page names every file and a second person can follow it without asking.
- [ ] **4.2 Five good-first issues.** Fixtures, a fault script, one example, one docs gap, one test.
  **done when** the repository has ≥5 issues labelled `good first issue`, each with a done-when.
- [ ] **4.3 The roadmap is public.** This page, linked from the README, with the same states.
  **done when** the README links it and the states match the issues.

## P5 — the article (not code)

- [ ] **5.1 One reproducible post.** The four rows, the command, the exclusions, and the one number that
      matters: writes per job. No money, only GPU-seconds and counts.
  **done when** a reader pastes the command and gets the same rows.

## What is demoted or removed (do these as part of P0/P1, not later)

| Thing | Action | Done when |
|---|---|---|
| README first screen | rewritten: `pip install` + contrast + deploy last | `make docs` passes and the order is visible |
| `docs/plan-4.2.md`, `docs/checklist-*.md` | moved to `/root/cg-internal-notes` (process, not product) | `docs/*.md` count drops by three and `make docs` passes |
| `deploy/` | frozen; no new work, still documented | README's deploy section is one screen |
| Certificate product, platform injection, adapter zoo, conformance | refused | no page or target appears for them |
| CLI's 19 verbs | first screen names three (`run`, `init`, `price`); the rest move to the install page | `ckptctl` with no arguments prints three groups |

## Order and why

P0 before P1: the contrast is reused as the README's evidence, the demo's fixture and the article's
table. P1 before P2: the callback asks the client, and the client must exist and degrade honestly. P2
before P3: the largest audience, and its 500-step default is the number the contrast is measured against.

## Not in this version

PyPI publication (until the precondition is decided), macOS/Windows wheels, a Python re-implementation
of the policy, the platform deployments, certificates, dashboards.

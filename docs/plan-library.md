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

- [x] **0.1 The three arms, on a virtual clock.** The policy lives in `internal/contrast`; the command
      in `experiments/contrast/main.go` drives the real `internal/loop` with an injected `Now`, so the
      run is deterministic and fast. Arm A simulates the framework default (`save every N steps`); arm
      B saves nothing; arm C is the real policy with nothing configured; arm C2 is the real policy with
      a bound. The writer costs `write-ms` of virtual time per save.
  **done when** `make contrast` prints four rows — `writes`, `io seconds`, coverage (announced stop,
      silent death) — and a test asserts four rows, the exact write counts, and that every row names
      its coverage.
  **observed** on a four-hour job (14,400 steps of 1s, a write costing 40s):
  ```
  500 steps (framework default)   28 writes   1120s   ≤500 steps   ≤500 steps
  nothing installed                0 writes      0s   everything   everything
  governor, unconfigured           1 write      40s   0            everything
  2h bound                         2 writes     80s   0            ≤2h
  ```
- [x] **0.2 The table is the README's first screen.** Install first, the contrast table second, Docker
      and Kubernetes below. Until the wheel exists (P1.1), the install line is the one that actually
      runs today — `install.sh` — and the page says so instead of promising `pip install`.
  **done when** `make docs` passes and the README's first three sections are install, the contrast,
      deploy.
  **observed** the first three sections are `Install`, `What the default costs`, `On a cluster`; the
      `docker build` line moved into `Install` as the second shape.
- [ ] **0.3 A one-liner for people without Go or Python.** The same command inside a container.
  **done when** `docker run …` prints the same four rows.
- [x] **0.4 What the framework defaults cost, per scale.** `internal/waste` + `experiments/waste`:
      the waste fraction is `T_write / (interval × t_step)`, and the interval is **the framework's
      actual default, not a round number**: Hugging Face `save_steps=500` with `save_total_limit=None`
      (keeps every copy, `training_args.py`); Lightning one epoch (`every_n_train_steps=None` and
      `every_n_epochs=None` means "at the end of every epoch") with `save_top_k=1`; Composer
      `save_interval='1ep'` with `num_checkpoints_to_keep=-1` (`checkpoint_saver.py`). Megatron has no
      default (`--save` requires an explicit `--save-interval`) and is printed as "no default".
      Report time and storage separately, and refuse to print a table without its assumptions line
      (bytes per parameter, throughput, step time, `steps-per-epoch`).
  **done when** `make waste` prints one row per scale, each naming the framework default it used and
      the source of that number, plus a test that the waste fraction equals `T_write/(interval·t)`
      and that a write longer than the interval is refused as infeasible.
  **observed** on the four scales (4h job, 16 B/param, 2 GB/s): Hugging Face 0.3% · 1.6% · 5.6% · **28%**,
      with 91 GB · 448 GB · 1.6 TB · **7.8 TB** kept; Lightning 0.2% · 0.8% · 2.8% · 14% and one copy;
      Composer 0.2% · 0.8% · 2.8% · 14% and every copy; Megatron "no default". The page is
      `docs/waste.md`, and its "what this does not claim" section is part of the artifact.

## P1 — the Python front door (engine bundled, not rewritten)

- [x] **1.1 A wheel that contains the engine.** `hack/build-wheel.sh` compiles `ckptctl` into
      `ckptctl_pytorch/_bin/`, then builds the wheel; the binary is package data, not a download.
  **done when** the script prints `wheel: …, engine inside` and the wheel lists the binary.
  **observed** `wheel: ckptctl_pytorch-0.2.0-py3-none-linux_x86_64.whl, engine inside`; the tag names
      the platform, because a `py3-none-any` wheel would install on a machine whose engine cannot run.
- [x] **1.2 The client starts the engine and reads the plan.** `Governor(start_engine=True)` launches
      `ckptctl run` for the directory, then `should_save()` reads `.ckpt-governor/state.json`.
  **done when** a clean virtualenv sees the plan appear and `should_save()` answers exactly when the
      plan says so.
  **observed** in a fresh venv with the wheel installed: `plan: True · engine running: True ·
      should_save: False · closed: True`; `make py` runs 17 tests, four of them new.
- [x] **1.3 Degradation when the engine dies.** The client does not print: a library that starts writing
      to stdout because a subprocess exited is a library nobody keeps. The plan simply ages out,
      `should_save()` answers false, and the reason is in `.ckpt-governor/engine.log`.
  **done when** a test kills the engine mid-run and the client neither raises nor claims a save.
- [ ] **1.4 The demo without Go.** `python -m ckptctl_pytorch.demo` prints the P0 table from the bundled
      engine.
  **done when** the demo runs in a clean virtualenv and prints four rows.
- [x] **1.5 The install page.** `docs/install-python.md`: install, three lines, what should appear, what
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

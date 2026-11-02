# Execution plan — the library direction

Companion to `docs/decisions/0028-the-direction-is-a-library.md`. Every line is a task with a **done
when** — a command and the string it must print. Placeholders are placeholders until a step prints real
numbers; a placeholder that survives into a release note is a lie.

**Language:** Go stays the engine. Python is the front door and the client; it ships the engine, it does
not re-implement it. Decision 0028, "two delivery shapes, one core".

## Preconditions (owner decisions, before P1.4)

- [x] **PyPI**: the package name, and whether publishing uses trusted publishing. Without a name, the
      wheel is built and tested locally; `pip install` from a URL stays documented but unverified.
  **observed** the name `ckpt-policy` is free (`https://pypi.org/pypi/ckpt-policy/json` answers 404),
      and publishing is **trusted publishing** from `.github/workflows/publish-pypi.yml`: PyPI verifies
      the workflow through `id-token`, so no token sits on a machine. The first upload is the
      pending-publisher form on PyPI and one `gh workflow run`; `RELEASE.md` carries both.
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
- [x] **0.2 The table is the README's first screen.** The contrast directly below the one-line claim, the
      install line next, Docker and Kubernetes below. Until the wheel exists (P1.1), the install line is
      the one that actually runs today — `install.sh` — and the page says so instead of promising
      `pip install`.
  **done when** `make docs` passes and the README opens with the claim, the contrast, and the install
      line.
  **observed** the order is the one-line claim, `The default is the problem` (the four-row table),
      `Install`, `In a training loop`, then `On a cluster`; the `docker build` line is the second shape
      inside `Install`. The table leads because the value is silence, which is P0's own premise, and
      the title already says what the product is.
- [x] **0.3 A one-liner for people without Go or Python.** The same command inside a container.
  **done when** `docker run …` prints the same four rows.
  **observed** `make contrast-image` on docker 29.1.3 prints the same four rows — 28/0/1/2 writes,
      1120s/0s/40s/80s io, the same coverage — in 2.6s. Two things were needed. The image now carries
      `/tmp` (`WORKDIR /tmp`), because the contrast makes its scratch directory there and a scratch
      image has none. And the scratch runs on `--tmpfs /tmp`: on overlay2 one round costs about 12 ms
      of fsync (`internal/advise/publish.go`), which turns a two-second table into minutes (1000 steps:
      0.6s on tmpfs, 12.3s on overlay2). At the controller's own cadence that 12 ms is 0.2% of a 5s
      round, which is why it is a note here and not a change to the loop.
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
      `ckpt_policy/_bin/`, then builds the wheel; the binary is package data, not a download.
  **done when** the script prints `wheel: …, engine inside` and the wheel lists the binary.
  **observed** `wheel: ckpt_policy-0.2.0-py3-none-linux_x86_64.whl, engine inside`; the tag names
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
- [x] **1.4 The demo without Go.** `python -m ckpt_policy.demo`: a synthetic loop, no torch, no Go on
      PATH, using the bundled engine. It is a two-arm smoke test, not the four-row model: the framework
      cadence against the policy, with the client's own per-step glue measured separately.
  **done when** the demo runs in a clean virtualenv and reports both arms.
  **observed** at 1000 steps and a 20ms write: cadence `2 writes · 0.04s io`, policy `1 write · 0.02s
      io`, and **`0.04s` overhead for 1000 steps** (40µs per `step()` call — one small file). That number
      is the first honest measure of what the client costs; it is small but not zero, and it is now
      printed rather than hidden.
- [x] **1.5 The install page.** `docs/install-python.md`: install, three lines, what should appear, what
      to do when it does not.
  **done when** `make docs` passes and the page is linked from the README.

## P2 — the audience: Hugging Face Trainer

The integration is `save_strategy="no"` plus a callback that sets `control.should_save` when
`should_save()` says so. Two lines, not a patch to `Trainer`; the latency is `logging_steps`, and the
announced stop is HF's own `enable_jit_checkpoint`.

- [x] **2.1 The callback, against a fake trainer.** CI has no torch, so the contract test drives a minimal
      object that models the Trainer's two relevant behaviours: its cadence, and `should_save` consumed
      at the logging boundary.
  **done when** a test asserts the default cadence writes twice over 1000 steps, the callback writes
      nothing while the plan is silent, and a request is honoured at the next logging step and only once.
  **observed** `make py` runs 23 tests; the four HF cases pass (`[500, 1000]` for the default, `[]` for
      silence, `[10]` for a request), and `on_train_end` records `ending`.
- [x] **2.2 The real run (torch only).** `make hf` runs a 1000-step CPU loop with `save_steps=500` and then
      with the callback. `PYTHON` selects the interpreter when torch lives in a virtualenv.
  **done when** it prints the checkpoint count for both arms and `trainer.state` is unchanged.
  **observed** with torch 2.13.0+cu130 / transformers 5.6.0:
  ```
  framework default (save every 500 steps)   2 checkpoint(s)
  ckpt-policy, unconfigured                  0 checkpoint(s) while training
  end write                                   True (final/)
  ```
  Transformers 5 calls **every** event on every callback, so the class subclasses `TrainerCallback`
      when transformers is installed and stays a plain class when it is not (the import stays optional).
- [x] **2.3 The supported-integration page.** The exact diff, the receipt to expect, and the exclusions.
  **done when** `make docs` passes and the page is linked from the README.
  **observed** `docs/huggingface.md` carries the diff, the four events table, and the two caveats
      (`logging_steps` latency; no public full-checkpoint API).

## P3 — the second and third integrations

Same shape, same contract test; the real runs need torch, so they stay manual targets.

- [x] **3.1 Lightning.** A callback that replaces `ModelCheckpoint`'s interval while the rest of
      Lightning stays untouched.
  **done when** a request is honoured at the next batch end, once, and the end is recorded.
  **observed** `make py` runs 28 tests; Lightning's four cases pass (silent `[]`, one request →
      `ckpt-step-1.ckpt`, a custom filename, `ending`). The latency is one batch because
      `trainer.save_checkpoint` is public — the contrast with the Trainer integration, whose latency is
      `logging_steps`.
- [x] **3.2 Composer.** A callback that asks the policy; `CheckpointSaver` is removed.
  **done when** a request is honoured at the next batch end, once, and the end is recorded.
  **observed** `make py` runs 31 tests; the three Composer cases pass (silent, one request with its
      filename format, `ending`). The real run is not exercised here (no composer in this environment);
      the page states what `CheckpointSaver` used to provide and no longer does.

## P4 — the contributor ladder

- [x] **4.1 The integration template.** One page: how to write a callback for framework X, what the
      receipt must contain, what the test must assert, which files to add.
  **done when** the page names every file and a second person can follow it without asking.
  **observed** `docs/integration-template.md`: the six rules, the skeleton, the fake-trainer test, and
      the five files an integration adds.
- [x] **4.2 Five good-first issues.** Fixtures, a fault script, one example, one docs gap, one test.
  **done when** the repository has ≥5 issues labelled `good first issue`, each with a done-when.
  **observed** #202–#206 created: a Lightning example, a Composer example, `demo --json` with a test,
      a Megatron row with an explicit interval, and the 100k-step glue measurement.
- [x] **4.3 The roadmap is public.** This page, linked from the README, with the same states.
  **done when** the README links it and the states match the issues.
  **observed** `docs/plan-library.md` is in the README's documentation list, and its checkboxes are the
      roadmap.

## P5 — the article (not code)

- [ ] **5.1 One reproducible post.** The four rows, the command, the exclusions, and the one number that
      matters: writes per job. No money, only GPU-seconds and counts.
  **done when** a reader pastes the command and gets the same rows.

## P6 — the community files (no code)

A repository is read before the product is used. Each file here answers a question a newcomer asks
anyway, and each one is checked by a link test rather than by memory.

- [x] **6.1 The package a first-time reader needs.** `docs/index.md` (one line per page, grouped by the
      question), a README of 586 words, "Start here" at the top of `CONTRIBUTING.md`, and
      `GOVERNANCE.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `CITATION.cff` at the root.
  **done when** every local link in the README and `docs/index.md` resolves, and `make docs` passes.
  **observed** `docs: 45 pages, 50 local links, 0 dead`; the README is 586 words and names the four
      pages a reader needs instead of listing thirty-one.
- [x] **6.2 Package metadata.** `pyproject.toml` carries the licence, the author, five `[project.urls]`
      entries, eight classifiers and five keywords, so a PyPI page says what the repository says.
  **done when** the wheel builds with that metadata and `make wheel` prints `engine inside`.
  **observed** the wheel's `METADATA` lists `License: Apache-2.0`, the Documentation URL
      (`docs/index.md`), and `Operating System :: POSIX :: Linux` — the platform the engine inside it
      can actually run on.
- [x] **6.3 The issues have a milestone.** #202–#206 are the contributor ladder; they now sit in one
      milestone instead of floating.
  **done when** `gh api repos/bolubo/checkpoint-governor/milestones` prints the milestone with five
      open issues.
  **observed** `1 v0.3 — the integrations open=5 closed=0`.

## What is demoted or removed (do these as part of P0/P1, not later)

| Thing | Action | Done when | State |
|---|---|---|---|
| README first screen | install first, the contrast table second, Docker and Kubernetes below | `make docs` passes and the sections are in that order | done — 586 words |
| `docs/plan-4.2.md`, `docs/checklist-*.md` | moved to `/root/cg-internal-notes` (process, not product) | `docs/*.md` count drops by three and `make docs` passes | done — 31 → 28, plus `index.md` |
| `deploy/` | frozen; no new work, still documented | README's deploy section is one screen | done |
| Certificate product, platform injection, adapter zoo, conformance | refused | no page or target appears for them | holding |
| CLI's 21 verbs | the help is three groups; the first is `init`, `price`, `run`, and every verb gets one line | `ckptctl` with no arguments prints three groups, and a test holds the list | done — three groups; `cmd/ckptctl/main_test.go` |

## Order and why

P0 before P1: the contrast is reused as the README's evidence, the demo's fixture and the article's
table. P1 before P2: the callback asks the client, and the client must exist and degrade honestly. P2
before P3: the largest audience, and its 500-step default is the number the contrast is measured against.

## Not in this version

PyPI publication (until the precondition is decided), macOS/Windows wheels, a Python re-implementation
of the policy, the platform deployments, certificates, dashboards.

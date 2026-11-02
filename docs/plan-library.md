# Execution plan — the library direction

Companion to `docs/decisions/0028-the-direction-is-a-library.md`. Every line is a task with a **done
when** — a command and the string it must print. Numbers are placeholders until the step prints real
ones; a placeholder that survives into a release note is a lie.

## P0 — the contrast (the growth engine)

The value is silence, so the first artifact is the counterfactual: the same fake job under three
policies, one command, no GPU and no cluster, under two minutes from a clone.

- **A, the framework default**: save every 500 steps.
- **B, nothing installed**: no writes at all.
- **C, governor with nothing configured**: one write at the end, one more if a stop is announced.

- [ ] **0.1 One command, three rows.** `experiments/contrast/` reuses the fake world; the step count,
      the write cost and the announced-stop point are flags.
  **done when** `make contrast` prints three rows, each naming writes, io seconds, and what it covers.
- [ ] **0.2 The same table is the first screen.** The README opens with the three rows and the command
      that prints them; the Docker and Kubernetes paths move below it.
  **done when** the README's first screen contains the command and the rows, and `make docs` passes.
- [ ] **0.3 One-liner for people without Go.** A container that runs the same command.
  **done when** `docker run …` prints the same three rows.

## P1 — the Python front door

The decision has to exist in the process that trains. Glue files are not a policy.

- [ ] **1.0 The policy core in Python.** A small, pure module: bound or none, announced stop, end of
      training, measured write time, step rate; output is `write now` / `skip` / `write at end` plus the
      reason. No new runtime dependency.
  **done when** `python -m unittest discover -s contrib -q` passes and the module imports nothing
      outside the standard library.
- [ ] **1.1 Golden cases, shared with Go.** One directory of scenario files consumed by both engines.
  **done when** `make golden` prints `golden: N/N — default, bound, announced, end` and a Go test and a
      Python test read the same files.
- [ ] **1.2 A demo without Go.** `python -m ckptctl_pytorch.demo` prints the P0 table from a machine with
      only Python installed.
  **done when** the demo runs in a clean virtualenv and prints the three rows.
- [ ] **1.3 The callback keeps working.** `Governor` keeps writing the glue files it writes today; the
      new `policy` object is additive.
  **done when** the existing 13 Python tests still pass.

## P2 — the audience: Hugging Face Trainer

- [ ] **2.1 One callback that replaces `save_steps`.** Opt-in, no change to `Trainer`'s public behaviour
      when disabled.
  **done when** a 1000-step CPU run with `save_steps=500` writes 3 times, and the same run with the
    callback writes once.
- [ ] **2.2 A supported-integration page.** The exact diff a user applies, the receipt they should see,
      and the exclusions.
  **done when** `make docs` passes and the page is linked from the README.
- [ ] **2.3 Nothing else in their loop changes.** No new required argument, no shard touched.
  **done when** the page's diff is three lines and the test run has no extra checkpoint files.

## P3 — second and third integrations

- [ ] **3.1 Lightning**: the same policy behind a `ModelCheckpoint` replacement.
  **done when** `make lightning` prints `lightning: 40 writes → 1, every-epoch default unchanged`.
- [ ] **3.2 Composer**: replace `save_interval='1ep'` and keep its `latest` pointer.
  **done when** `make composer` prints `composer: 40 writes → 1, latest resolves`.

## P4 — the contributor ladder

- [ ] **4.1 An integration template.** One page: how to write a callback for framework X, what the
      receipt must contain, what the test must assert.
  **done when** a second person can follow it without asking a question (the page names every file).
- [ ] **4.2 Good first issues.** Fixtures, fault scripts, one example per framework, docs.
  **done when** the repository has ≥5 issues labelled `good first issue`, each with a done-when.
- [ ] **4.3 A public roadmap.** P0–P3 above, in the repository, not in a session.
  **done when** the plan page is the roadmap and the README links it.

## P5 — the article (not code)

- [ ] **5.1 One reproducible post.** The three rows, the command, the exclusions, and the one number
      that matters (writes per job). No money, only GPU-seconds and counts.
  **done when** a reader can paste the command and get the same rows.

## What is demoted or removed

| Thing | Action | Why |
|---|---|---|
| Platform injection (`deploy/platform`, plan-platform P2) | **removed from the plan** | needs a cluster to show value; not a community unit |
| Certificate product | **removed from the plan** | revenue-shaped; the receipt inside the library is enough |
| Adapter zoo, conformance kit | **refused** | surface without evidence (0020) |
| `docs/plan-platform.md` | **superseded** | kept as history for the installer/payer analysis |
| 0027 | **superseded** | the analysis stands, the sequencing does not |
| README first screen | **rewritten** | a Python user must see `pip install` and the contrast first |
| `deploy/` | **frozen** | it still works; no new work goes there |

## Order and why

P0 before P1: the contrast is the artifact every later step reuses as a test and as evidence. P1 before
P2: the decision must be in-process before a callback can ask it. P2 before P3: the largest audience
first, and its default is exactly the number the contrast is measured against.

# The gate, redesigned

Status: design, 2026-11-02. Replaces the single sequential `verify` job that ran the whole suite on every
push. The complaints were "the CI is always red" and "every change takes three minutes"; this page says
what was actually happening, and what the gate is now.

## What "red" actually was

Forty runs in fifty minutes, counted from the API on 2026-11-02:

| fact | number |
|---|---|
| runs | 40 |
| success | 36 |
| failure | 2 |
| in progress when counted | 2 |
| wall time per run | 176–290s |
| runs created per push | **2** (one `push`, one `pull_request`) |

Both failures failed at **`make check`**, not at the integration steps:

```
--- FAIL: TestTheVerifierTurnsAMismatchIntoARepair (0.00s)
    loop_test.go:853: the verifier did not record the mismatch
```

That test sets `VerifyEvery: time.Nanosecond` while handing the loop a **frozen clock**. The gate in
`internal/loop` compares `now.Sub(LastVerifyAt) < every`; with a clock that does not move, whether the
verifier is due depends on the nanoseconds the process happened to spend — a test that passes fifty times
locally and fails on a different machine. It is a test-design defect, not a product defect, and it is the
one failure class that turns a green suite red at random.

Two honesty notes, because the claim should be falsifiable:

- **The test is fixed now.** It asks for an explicit interval and advances its own clock, so the refusal
  under test is the only variable; `-count=30` and `-shuffle=on -count=2` are green. The failing run
  itself was a merge of an intermediate `main`, so "random" remains an inference — what is certain is
  that a frozen clock plus a nanosecond interval cannot be deterministic, and that is gone.
- **The other half of "always red" is not red at all.** With branch protection requiring **up to date**,
  every push to `main` by another session made our PR `BEHIND`, which GitHub shows next to a green check.
  That was fixed on the repository (strict off), not in the workflow.

## What the gate is now

One required check, `verify`, and it does not always do the same work:

| what changed | what runs | wall time (measured on the runner) |
|---|---|---|
| only Markdown / `docs/` | `make docs`: compile one package, two pins | one step; ~35s targeted |
| Go, shell, tests | `make fast`: format check, vet, build, every unit test | 110–115s |
| behaviour (Go, `experiments/`, `cmd/`) | also `e2e`, `exp`, `demo`, `install-smoke`, `spot`, `resume`, `py`, `drill` | 184s |
| `Dockerfile`, `deploy/` | also `docker build` | +40s |
| a tag | `release.yml`, not this workflow | — |
| a manual dispatch | everything, including the image | — |

**The numbers are why the first cut was wrong.** The first version ran `make fast` for every change and
added `make docs` on top; a documentation-only PR measured **184s**, because the unit suite is the
expensive part and it had nothing to say about a page. The table above is the second cut: a docs change
runs one step. The `code`/`docs` split is now also read from the API (`pulls/{n}/files`,
`commits/{sha}`), not from merge-base arithmetic on a shallow clone: the first version's two runs on the
same branch disagreed about which steps to run, which is exactly the kind of silent scope bug the one-job
design must not have. A commit the API will not describe is treated as code.

Two rules make that possible without a matrix and without third-party path filters:

1. **The workflow classifies the diff itself.** A shell step asks the API for the changed files and writes
   `code`, `docs` and `image` outputs; every expensive step carries `if: steps.scope.outputs.code ==
   'true'`. No `dorny/paths-filter`, no action whose supply chain we would have to trust.
2. **The requirement stays one check.** Steps inside one job mean one required context, so a scope
   mistake cannot leave a PR "green because nothing ran": anything that skips is printed (`changing
   docs only; the integration steps are skipped`) and the job still ends in `make docs`.

Other changes, each for a specific failure it removes:

- **`pull_request` and `push` are narrowed.** `pull_request` targets `main`; `push` only `main`. The old
  `push:` with no branches ran twice for every branch, which is half the minutes in the table above.
- **`concurrency: cancel-in-progress`.** A second push to the same branch cancels the first run instead
  of paying for both.
- **`timeout-minutes: 20` on the job, `-timeout 10m` on `go test`.** A hung test used to sit for the
  six-hour default; the budget is now ours.
- **Formatting is checked, not written.** `make fmt-check` fails on a file `gofmt` would change; the old
  `make check` rewrote files and passed, which meant a push could be green while `main` was unformatted.
- **`docker build` moved behind the `image` scope**, and out of `pull_request` for a Markdown change.
- **No schedule.** The heavy checks are a **manual `workflow_dispatch`**, because this repository does
  not create cron jobs (the workspace rule), and a period gate is the shape of a job somebody stops
  reading.

## Rules for the tests that feed it

A gate is only as good as its determinism, so the rules that keep it honest are part of the design:

1. **A test may not read the wall clock.** Time comes from the loop's injected `Now`. `VerifyEvery:
   time.Nanosecond` against a frozen clock is exactly the flake above, and the fix is in the test:
   either advance the clock or ask the loop to verify explicitly.
2. **No network, no loopback.** The machine's WSL blocklist has already taught us that a test needing
   127.0.0.1 is a test that fails where we cannot see it; the metrics and notify tests inject a
   recorder/`RoundTripper` instead.
3. **Budgets are named.** Every rig script runs under `BUDGET` (`timeout`), so a hang prints where it was
   instead of burning a runner.
4. **A flake is hunted, not retried.** `make flake` runs `-count=20 -shuffle=on` on the timing-sensitive
   packages. Retrying a red step in CI would hide the defect this page just named.

## Local, in the same order

| command | what it is |
|---|---|
| `make fast` | the 60-second loop: format check, vet, build, all unit tests |
| `make docs` | the docs pins, ~15s |
| `make ci` | the full gate, the same steps CI runs for a code change |
| `hack/prepush.sh` | the same steps, in the same order, naming the one that failed |

`make verify` stays the release-grade gate (check, e2e, parity, spot, resume, py, drill, and the four
pinned tests); `make ci` remains the honest statement of what the workflow does.

## What is not in CI, and why

- **The multi-arch image** (`linux/amd64` + `linux/arm64` under QEMU): minutes, and its only consumer is
  a release. `release.yml` does it on a tag.
- **The cluster experiments** (`experiments/kueue`, the CLI rig's cluster rows): they need a cluster,
  and the promise is a laptop gate.
- **The real-clock smoke** (`make smoke MINUTES=10`): a bound that only a machine with real time can
  break is already checked in five seconds by the virtual day in `internal/loop`.
- **A nightly anything.** The workspace forbids creating scheduled jobs; the heavy path is
  `workflow_dispatch`, run by a person when they want it.

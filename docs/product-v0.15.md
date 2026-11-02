# Product v0.15 — the release that can be installed, and proved

Status: released as `v0.15.0`, 2026-11-02. v0.15 is a **compatibility release**: the decision core does
not change; what changes is how a stranger gets the thing, and what the thing promises to keep working.
The ML-facing work — the training-script callback, the resume contract, time-to-resume — is v0.2, and is
named at the end of this page so it does not leak into this one.

Why this shape, in one paragraph. The reference controllers' second editions did three things and no
fourth: more inputs, an operator-facing surface, and hardened contract names — and none of them rewrote
the core (`docs/first-edition-parity.md`). This repository already shipped the first two edges (metrics,
an outward event, conditions from disk). What is missing is the third: a written promise about what a
user's directory, plan file, metrics and CLI will keep meaning across an upgrade — plus an install that
does not need us in the loop.

## 1. The goal, as a sentence a stranger can check

> Five minutes from `curl` to a receipt: one binary on the PATH, a directory, one command, and a printed
> statement of what was decided and what a loss would cost.

If that takes more than five minutes, or needs a message to us, the install (and therefore the
distribution) is wrong — not the product.

## 2. What ships

### 2.1 The contract (the reason this is a version, not a commit)

- **The plan schema is stated, not implied.** `state.json` carries a schema number, `ckptctl doctor`
  prints `plan schema 2, this binary reads 2`, and a plan outside the range is refused with its number
  named (exit code 2).
- **A checked-in fixture of the previous build.** A directory the real binaries wrote is part of the
  test data (`internal/advise/testdata/schema-2`); reading it must produce the same decision, and a run
  on it must continue the ledger rather than start over.
- **The frozen lists** — condition names, event kinds, metric names, exit codes, flags — in
  `docs/compatibility.md`, pinned by `TestFrozenNames`, so a rename is a decision and not a typo.

### 2.2 The proof (measurement and fault injection, one rig)

The rig already runs real clocks and real files. v0.15 adds the one fault this product exists for:

- **a spot simulator**: `SIGTERM`, a grace period, then `SIGKILL` at the deadline, using the real
  binaries (`ckpttrainer --grace`, `ckptctl run`).
- each scenario ends in a **receipt**, not a row of `PASS`:

```
spot preemption at 12s (grace 30s)
  one write: full, sealed 8.4s
  may-exit written at step 1180
```

### 2.3 The install (one command, no Go, no container)

- `install.sh` downloads the release tarball for this architecture, **verifies `SHA256SUMS`**, installs
  both binaries to `${PREFIX:-$HOME/.local/bin}` and runs `ckptctl version` as a self-check.
- The script's release base is overridable (`CKPTCTL_RELEASE_BASE`), so CI can prove the whole path
  offline against a locally built `dist/` — otherwise this acceptance line would be "verified next
  release", which is the same as unverified.

### 2.4 The release (what their second editions all had and we do not)

`CHANGELOG.md` per release, `docs/install.md` (single binary / container / cluster manifest plus the
files a job writes), a self-contained `deploy/` (manifest, example `conditions.json` and
`deployment.json`, a world fixture), and `make release-notes` feeding the tag workflow.

## 3. Acceptance lines

Each line is printed by a command, not asserted in prose.

| # | Line |
|---|---|
| 1 | `make dist` produces a binary and `SHA256SUMS`; `install.sh` with a local `dist/` prints `installed ckptctl <version>` and the binary answers `ckptctl version` |
| 2 | `ckptctl doctor --dir` prints `plan schema 2, this binary reads 2`; a `schema 99` file exits 2 with the number named |
| 3 | the schema-2 fixture reads back as the same decision (`adopted: schema 2, decision skip (calm)`) |
| 4 | `make spot` prints `preemption → one write → may-exit (step N)` |
| 5 | `TestFrozenNames` prints `frozen: 14 conditions, 9 event kinds, 6 metrics, 2 exit codes` |
| 6 | every entry in `CHANGELOG.md` for v0.15 corresponds to a merged PR, and `make release-notes` prints that section |

## 4. Not in v0.15

No CRD or operator, no scheduler, no fleet-wide view, no new condition families (`conditions.d` is the
extension point), no dashboard, and no second decision path. No Python: the training-script callback is
v0.2, because a callback's value is inseparable from what it can restore.

## 5. Deferred to v0.2, on purpose

- **The training-script callback** (`contrib/ckptctl-pytorch`): one line in a loop, writing
  `step-rate`/`ending`/`stopping` and reading the plan.
- **The restore side.** This product decides *when*; it does not own *what a checkpoint must contain*
  to resume. Whether to verify resumability (optimizer state, world size, RNG position) rather than only
  reading the bytes back is the open question that decides what v0.2 is for.
- **Time-to-resume** as a printed number, and the portable-write question when a spot reclaim changes
  the world size.

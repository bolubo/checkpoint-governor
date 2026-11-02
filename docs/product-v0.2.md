# Product v0.2 — the release that can be installed, and proved

Status: scope, 2026-11-02. Not released. The v0.2 target is a **compatibility release**: the decision
core does not change; what changes is how a stranger gets the thing, and what the thing promises to
keep working.

Why this shape, in one paragraph. The reference controllers' second editions did three things and no
fourth: more inputs, an operator-facing surface, and hardened contract names — and none of them rewrote
the core (`docs/first-edition-parity.md`). This repository already shipped the first two edges (metrics,
an outward event, conditions from disk). What is missing is the third: a written promise about what a
user's directory, plan file, metrics and CLI will keep meaning across an upgrade — plus an install that
does not need us in the loop.

## 1. The goal, as a sentence a stranger can check

> Five minutes from `curl` to a receipt: one line in a training script, a simulated preemption, one
> write, and a printed statement of what was lost.

If that takes more than five minutes, or needs a message to us, the install (and therefore the
distribution) is wrong — not the product.

## 2. What ships, in four parts

### 2.1 The integration (the distribution unit)

`contrib/ckptctl-pytorch/`, 30–40 lines, no third-party dependency:

```python
from ckptctl import Governor
gov = Governor("/ckpt/my-job")           # points at a directory; starts nothing
for step in ...:
    gov.step(step)                        # writes .ckpt-governor/step-rate
    if gov.should_save(step):             # reads the published plan; never decides
        save_checkpoint(step)
gov.ending()                              # writes .ckpt-governor/ending
```

The contract with the job (extends `docs/glue.md`):

| Who | Does | Never does |
|---|---|---|
| the callback | writes `step-rate`, `ending`, `stopping`; reads `state.json` | decide, copy conditions, retry a write |
| the Go process | observes, decides, publishes the plan, settles attempts | run inside the training loop |

On SIGTERM the callback writes `stopping` and waits for `may-exit` with a bounded timeout, then prints
the last step it knows is on disk. A `state.json` it cannot parse is *not* guessed at: the job falls
back to its own `no-us`.

Three install shapes share one binary: a single static binary, a container, and the callback (which
shells out to the binary or installs it).

### 2.2 The contract (the reason this is a version, not a commit)

- **Plan schema, frozen and versioned.** `.ckpt-governor/state.json` carries a schema number; `ckptctl
  doctor` prints `plan schema N, this binary reads N..M`. Fields are additive within a schema; removing
  one is a schema bump with a migration note.
- **A checked-in fixture of the previous version.** A directory written by v0.1 is part of the test data;
  v0.2 reading it must produce the same decision. The reverse — an older binary meeting a newer plan —
  must fail loudly rather than misread.
- **One frozen-names page**: metric names (`checkpoint_*`), `events.jsonl` `kind`/`code`, exit codes, CLI
  flags, the 14 condition names. A test pins them, so a rename is a decision, not a typo.
- **Upgrade semantics**: "old process wrote, killed, new process took over" — attempts, ledger, trash,
  lease. The crash-point tests already exist; this is the same tool across a version boundary.

### 2.3 The proof (measurement and fault injection, one rig)

The rig already runs real clocks and real files. v0.2 adds the one fault this product exists for:

- **a spot simulator**: `SIGTERM`, a grace period, then `SIGKILL` at the deadline, and the world record
  the job would see.
- each scenario ends in a **receipt**, not a row of `PASS`:

```
spot preemption at 12s (grace 30s)
  one write: full, sealed 8.4s
  resumed at step 1180 (12 steps lost, 0.9%)
  no-us 10m · with-us 30m · 3 writes were possible, 1 was made
```

### 2.4 The release (what their second editions all had and we do not)

`CHANGELOG.md` per release, `docs/install.md` (binary / container / callback), a self-contained
`deploy/` (manifest, example config, a world fixture), and `make release-notes`.

## 3. Acceptance lines

Each line is printed by a command, not asserted in prose.

| # | Line |
|---|---|
| 1 | `make dist` produces a binary and `SHA256SUMS`; `ckptctl run --once` on an empty directory prints a receipt within 5s |
| 2 | the example training script under a simulated preemption prints `preemption → one write → resumed at step N` |
| 3 | `ckptctl doctor --dir` prints `plan schema 2, this binary reads 2..3` |
| 4 | the v0.1 fixture is read by the v0.2 binary and produces the same decision (compatibility test) |
| 5 | `make parity`, `make exp`, `make verify` pass; `serve --metrics` shows the window's remaining bound |
| 6 | every entry in `CHANGELOG.md` for v0.2 corresponds to a merged PR |

## 4. Not in v0.2

No CRD or operator, no scheduler, no fleet-wide view, no new condition families (`conditions.d` is the
extension point), no dashboard, and no second decision path. And not "another checkpoint library": that
shelf is empty of readers, and it would drop the only thing this product has that a library cannot —
the decision, with evidence.

## 5. Open, and deliberately not decided here

- **The restore side.** This product decides *when*; it does not own *what a checkpoint must contain* to
  resume. Whether v0.2 should also verify resumability (optimizer state, world size, RNG position) —
  rather than only read the bytes back — is an open question, and it is the one most likely to change
  what the next release is for.
- **The name.** The identifier stays `checkpoint-governor`; the tagline changes to the value, because
  the word "checkpoint" is the search entry, not the product. Revisit only if the ML audience reads it
  as "the framework's resume feature".

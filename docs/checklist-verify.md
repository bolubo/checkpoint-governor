# Execution checklist — verify and demand (v0.2)

Companion to `verify-and-demand.md`. Every line is a task with a **done when**, because "implement the
verifier" is not a task — it is a wish. The order matters: the contract comes first because everything
else asserts against it, and the classifier comes before the rewrite because you cannot demand correctly
until you can name what is wrong.

Deliberately **not** in this version: the restorable check (load it and continue a step). That belongs to
the ML stage, where a checkpoint has a loader we can drive. Until then `verified` means "the bytes match
the manifest", and the receipt says so.

---

## Phase 0 — the contract (nothing works before this)

- [x] **0.1 Manifest schema, written down.**
  `generation`, `step`, `sealedAt`, `shards[{name,size,sha256}]`, `writer`.
  **done when** the schema lives in `docs/verify-and-demand.md` and a Go struct round-trips a fixture
  byte-for-byte.

- [x] **0.2 Request schema, written down.**
  `{"generation", "reason", "notAfter", "full"}`, written to a temporary name and renamed.
  **done when** a half-written request file is provably unreadable as a request (a test writes 12 bytes
  and the reader refuses).

- [x] **0.3 A fixture generator.**
  Small trees under `testdata/`: `sealed-ok`, `short-shard`, `torn-tail`, `no-manifest`, `stale-generation`,
  `evil-path`.
  **done when** each fixture has a one-line comment saying which state it is meant to produce.

## Phase 1 — the classifier (read-only, no decisions)

- [x] **1.1 Structure check.**
  Parse the manifest, list shards, compare sizes, compare generation with the request.
  **done when** each Phase-0 fixture lands in its named state, and an unparseable manifest is `broken`
  rather than an error that escapes.

- [x] **1.2 Sample check, tail-first.**
  Hash the last min(size, 1 MiB) of every shard and compare it with the manifest's `tailSha256`.
  **done when** `torn-tail` (right size, wrong bytes at the end) is caught, and a clean tree passes in
  milliseconds on a fixture of at least a few hundred MB.
  **Found while building it:** the manifest had nowhere to put the tail hash, so the cheap check had
  nothing to compare against — a size check dressed as a content check. `tailSha256` is now part of the
  contract, and a writer that omits it gets `broken/hash` from the cheap path rather than a pass.

- [x] **1.3 Untrusted input rules.**
  Reject absolute paths and `..` in shard names; cap shard count and declared sizes; never use `sealedAt`
  to decide whether a deadline passed.
  **done when** the `evil-path` fixture is refused with a message naming the offending entry, and a
  manifest declaring 4 TB on a 100 MB test tree is refused.

- [x] **1.4 The "in flight" distinction.**
  A generation directory with no manifest is `waiting` until the deadline, then `unsealed-expired`.
  **done when** both halves are tested with a controllable clock — no `sleep` in the test.

## Phase 2 — the receipt says what happened

- [ ] **2.1 Three states, no fourth.**
  `requested` / `sealed` / `verified`, plus the failure reasons from Phase 1.
  **done when** no code path can print `verified` without a successful sample check, enforced by a test
  that walks every construction site.

- [ ] **2.2 One line per generation, with the numbers.**
  `generation 7 requested … sealed … verified … sample steps … bytes …`, and on failure the attempt and
  the reason.
  **done when** `ckptctl` prints it for a fixture tree, and the fields come from the files rather than
  from the decision.

- [ ] **2.3 Reword the retention sentence.**
  `policy.md` currently says the implementation *deletes* sealed copies older than a step.
  **done when** it says *recommends deleting up to generation N*, and the owner executes it.

## Phase 3 — demand a rewrite

- [x] **3.1 Rewrite = generation + 1, never a retry of the same one.**
  **done when** a late writer finishing generation 7 cannot satisfy a request for 8, proven by a test
  that seals 7 after 8 was requested.

- [x] **3.2 Reason → action.**
  `broken-short` → rewrite; `broken-hash` → rewrite **from scratch**; `broken-stale` → ignore;
  `cannot-fit` → **stop and report**.
  **done when** each reason has a case, and `cannot-fit` provably does not loop.

- [x] **3.3 Budget.**
  Attempts and elapsed time both bounded; exhaustion yields `given-up` in the receipt.
  **done when** a writer that always fails produces exactly N attempts and one `given-up` line.

## Phase 4 — the experiment that is the product

- [x] **4.1 Write bad → detect → rewrite → verified.**
  A trainer is killed mid-shard at generation 7; the classifier says `broken-short`; a request for 8 goes
  out; the writer completes; the classifier says `verified`.
  **done when** the run prints those four lines in order, **and asserts that no intermediate state was
  ever recorded as a success**. This is the single case that proves the product does what it claims.
  **Built as a test rather than a printout** (`loop_test.go`): a fake writer dies mid-shard, the classifier
  says `short`, the decision asks for generation 8, the rewrite lands and verifies — and the replaced copy
  is then judged `stale` against the new request, so it can never be mistaken for the answer. A second test
  keeps the failing writer failing and asserts the loop gives up exactly at the budget.

- [ ] **4.2 The fault matrix, all against real files.**

  | fault | expected |
  |---|---|
  | kill mid-shard | `broken-short` → rewrite → `verified` |
  | torn tail | `broken-hash` → rewrite from scratch |
  | disk full | `cannot-fit`, no retry storm |
  | slow `fsync` | the deadline fires, no hang |
  | volume unmounted mid-write | `waiting` → `unsealed-expired` |
  | two writers, one directory | one `verified`, no spurious `broken` |
  | `../` in a shard name | refused, loudly |

  **done when** the matrix runs in the rig and its rows are the ones a release note can cite.

## Phase 5 — wire it up

- [ ] **5.1 `explain` answers "why is this checkpoint not verified".**
  Same door-list discipline as `why-no-write.md`: the first thing that refused is the answer.
- [ ] **5.2 Metrics.** `P(verified | a unit was taken)`, attempts per preemption, work lost in seconds
  (as a range), invalid-checkpoint rate (must be 0).
- [ ] **5.3 The integration contract, one page for the writer's author.** Save-on-demand, seal-last, the
  manifest schema, and what we do when it is late.

---

## Deferred to the ML stage (2.0)

- **Restorable check** — load the checkpoint and continue one step. This is the only check that proves
  resumability, and it needs a loader we can drive.
- **Full-hash cadence** — when to pay for a complete hash instead of a sample.
- **Retention execution** — if the owner ever wants us to delete rather than recommend.
- **Format awareness** — shards stay opaque in v0.2, deliberately.

## What this version therefore claims

> We asked for a write, we checked what landed, and we said which of the three it is. If it was wrong, we
> asked again, with a new generation, until the budget ran out — and then we said so.

Nothing in that sentence promises that a job can resume. That promise arrives with the restorable check,
and not before.

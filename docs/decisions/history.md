# History: the records that stopped being files

Each paragraph is one decision that was written down, used, and then folded here when its page
stopped carrying weight. The full text of every entry is in git history:

```
git log --follow -- docs/decisions/0001-the-unit-is-a-directory.md
```

## 0001 — The unit is a directory, not a pod

A training job may run as one pod, a gang of ranks, or a batch of jobs. The algorithm sees only
`UnitID` and `Generation`; the implementation decides what one writable unit is. A unit is a checkpoint
directory, which is why `ckptctl run --dir` needs nothing else, and why `supervise` discovers units as
directories with a `.ckpt-governor` marker. Still true; the vocabulary moved to
[product-v1.md](../product-v1.md).

## 0002 — Unknown values where the contract has no sentinel

`-1` was the sentinel for unknown on the numeric `Snapshot` fields. The fields without a sentinel say
"unknown" through a separate boolean or a three-state type, never through a plausible zero — the rule
that survives in `Evidence` (absent vs unknown) and in `CopyUnverified`.

## 0003 — Jitter belongs to the policy, not the state file

Many jobs falling back at the same moment would synchronise; the decision was that a policy owns any
jitter it wants. With `no-us` the job's own timer is its own business, so the question dissolved.

## 0004 — An abandoned write is detected by quiescence, not duration

A directory without a manifest has stopped being a write in progress when nothing has changed for long
enough, not when a fixed duration has passed: a large checkpoint on a busy disk is slow, not dead. The
quiescence rule is in `internal/observe` and is what lets a later round settle the attempt instead of
guessing.

## 0005 (capacity) — The capacity headroom is 1.1, not 1.5

The factor was a guess for temporary shards. Replaying a 2 GB volume with 900 MB checkpoints exposed
it: with one required copy the rule asked for more free space than could exist and answered
`infeasible` forever. `1.1 × CopiesToWrite · CheckpointBytes` is the number the delivery layer uses;
the storage condition keeps its own `2×` rule because it describes a different moment.

## 0005 (copy identity) — A copy has a location, and retention counts steps

A copy was `{Step, Bytes, State, CreatedAt}`, which quietly assumed one copy per step. The proposal
splits identity (step plus path) from retention (steps plus a floor); the floor exists today as
`KeepCopies` and the "last copy is never deleted" rule, and the fuller identity is still open.

## 0006 — The boundary: a capability is declared once, produced once, replayed once

A condition has one declaration, one producer and one replay path; a second representation of the same
fact is drift waiting to happen. Package names have moved (`signal` → `observe`, `producer` →
`produce`), and the interval-era modules are gone, but the rule holds: `internal/policy/condition.go`
is the single declaration and `adapters` the single producer.

## 0010 — Layers, capabilities, structured explanations

The structure — contract / core / factors / one composition / registry, capabilities that can only
deny, a closed reason vocabulary, divergence reported rather than absorbed — is what the risk core is
built in. Its *content* (a period from `L`, `τ` and `W`; delta and async as scheduling) is dead with
the interval algorithm.

## 0011 — The frozen V1 contract

The authority for the V1 input and output: every field, why it exists, what fills it. The
interval-shaped fields (`MaxLostWork`, `MaxTax`, `Interval`, `TriggerAt`, `Tax`, `TightestBound`,
`FallbackInterval`, `DeltaShare`, `FullWriteSeconds`) are removed; the unknown-versus-zero rule, the
job-local facts and the `Evidence` channel survive in `internal/policy/policy.go`.

## 0014 — Why the product moved from intervals to risk conditions

The interval design optimised expected lost work against a tax budget — a quantity the user cannot
observe, computed from a failure rate nobody can measure. Its own arithmetic showed the best a
constant period can be is a few percent away from the best an oracle could do, and it was blind to
every fact that actually precedes a loss. The replacement asks one boolean question — *is there an
event after which this job's work would be lost, and is there still time to save it?* — and the
catalogue answers it. This record is the reasoning a later reader needs before proposing arithmetic.

## 0016 — The delivery side is five planes, and an attempt is the unit

`producer → Evidence → advisor → Decision → executor → (state file, ledger, events) → view`, one
direction of data, no plane reading another's internals. Three non-negotiable rules: **time belongs to
the executor** (the advisor receives `Now` as data), **an attempt is the unit of accounting** (one
request, one identity, one immutable outcome), and **execution is asynchronous** (a round starts an
attempt and returns). The planes are still the shape of `internal/{produce,observe,policy,exec,loop}`;
the summary is [design.md](../design.md) §2–§5.

## 0018 — The first mile: three defaults and one sentence

A person who never configured anything meets defaults, not a contract. Four unwritten ones were being
re-argued: `no-us` defaults to `none` and the run says so on its first line; the recommended window is
one write at the middle of the run; `init` writes no file when the answer is "skip"; and the receipt
always states the bound, including when `with-us` is unset. All four are in
[product-v1.md](../product-v1.md) §5 and §13.

## 0019 — The detection bound: what the probe intervals cost

"An announced death is paid for by one write" has a second half nobody had written down: how long it
takes to notice the announcement. The bound is the observer's interval plus the probe's poll, plus
cluster reads, publication and controller work — so a polling interval alone does not guarantee a
completed checkpoint in time, and a death with no announcement has no guarantee at all. The arithmetic
is now [conditions.md](../conditions.md) "Probe intervals, and the detection bound".

## 0020 — The accounting is derived, not exported

The numbers the product owns are about its own behaviour (writes and what each cost, failures it
survived, plans the ledger never recorded, checkpoints read back); a metric about the training job
belongs to the job's monitoring. `ckptctl stats --dir` reads the files, because the most common time to
ask is after the controller is gone, and a scrape target is silent then.

**Amendment (2026-11-02).** `ckptctl serve --metrics` now exists: a read-only Prometheus endpoint for
staleness, over-bound seconds, blind conditions and writes by kind, one walk per scrape, no lease and no
decision. It does not overturn this record — the after-the-fact accounting is still `stats`, the product
still counts nothing about the training job, and there is still no dashboard in it. The refusal that
died was "no listener at all", not "no accounting".

## Status

Everything above is accepted history. A change that contradicts one of these paragraphs needs a new
record in this directory, not a silent edit here.

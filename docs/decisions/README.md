# Decisions

One file per decision that is still load-bearing, plus one history page for the rest. A record here
answers "why is it this way", so a later change either follows it or replaces it with a new record.

## The rules

1. **A record is written when a decision is made, not when it is convenient.** Copy `0000-template.md`.
2. **Status is one of: proposed, accepted, superseded.** A superseded record keeps its number and says
   what replaced it; it is never deleted while the code still cites it.
3. **Every decision a Go file cites must exist here.** The citation is `docs/decisions/<number>` or
   `decision <number>`; if the record moves to `history.md`, the citation moves with it.
4. **Historical records are one paragraph in `history.md`.** Their full text is in git history
   (`git log --follow -- docs/decisions/`); folding them keeps the tree readable without pretending
   the reasoning never happened.
5. **No duplicate numbers.** If two records need the same number, the older one is folded into
   `history.md` rather than renumbered — a number that moves is a broken citation.

## Live records

| # | Title | Why it still matters |
|---|---|---|
| 0013 | [The catalogue of conditions](0013-risk-conditions.md) | the 14 names, three tiers, two missing policies, precedence: the contract every producer and the policy share |
| 0015 | [The risk core](0015-risk-core.md) | the rule `risk-v1` implements, and the fields of `Snapshot`/`Bounds` it may read |
| 0017 | [Two numbers](0017-two-numbers.md) | `no-us` / `with-us`: what the user answers and what the executor owns |
| 0019 | [Deployment without a custom resource](0019-deployment-without-crd.md) | the installation: one process, one manifest, one directory root — and the three conditions that would reopen the CRD question |
| 0020 | [What this is not](0020-what-this-is-not.md) | no CRD, no scheduler, no delta, no fleet-wide aggregator |
| 0021 | [Fleet writes](0021-fleet-writes.md) | one cause, several units: the permit verdict and `one-per-victim` fanout |
| 0022 | [Fanout and epochs](0022-fanout-and-epochs.md) | one decision per event: the cause id, the queue epoch, and why a paid event is not paid twice |
| 0023 | [The eviction rule is a conjunction](0023-the-eviction-rule-is-a-conjunction.md) | when `risk.eviction-likely` fires: a held-back job **and** a shortfall **and** a queue that may preempt, instead of an *or* of pressure signals |
| 0024 | [Conditions from disk](0024-conditions-from-disk.md) | the one extension point: a name, a file probe, and why a built-in wins a tie |

## Folded records

`history.md` carries one paragraph each for 0001–0011, 0014, 0016, 0018, 0019 (detection bound) and
0020 (accounting). Read it when a comment or a page says "decision 0014" and the detail matters.

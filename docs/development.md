# How to work in this repository

## Two layers that move separately

- `internal/policy` — the algorithm. Experiments are allowed and expected here.
- everything else — delivery: reading the directory, writing the state file, wiring the controller.
  This layer changes only when the interface changes.

The test of a good boundary: replacing the algorithm must not touch any other file. If it does, the
interface is wrong.

A second boundary is just as strict: the arithmetic that *prices* a decision (`internal/bound`,
`internal/audit`) and the simulator (`internal/simulate`) never decide anything. They may be deleted
without changing one write, and a review should check that property before merging anything into them.

## Rules

1. **A claim needs a printed number.** "Faster", "safer", "better" are not results. A table of `V`
   and `T` is.
2. **Unknown is not zero.** A missing sample is reported as unknown, and every rule that needs it
   degrades explicitly.
3. **Checks are timed.** Unit tests: a second or two. Replay of 1000 traces: seconds. Tests that
   need a cluster: minutes, and they run once per change, not in a loop.
4. **The policy never reads the clock, the disk, or the cluster.** Everything arrives in its input.
5. **One milestone, one commit**, in English, stating what changed and the number that proves it.

## Document policy

The document set is organized by the question a reader has, and each answer lives in exactly one
place:

```
README.md                     what this is, how to run it, where to read next
docs/product-v1.md            the user-facing surface: what is asked, shown, claimed
docs/design-backstop.md       why the product is this shape (two numbers, one boolean, a window)
docs/design-v1.md             how it is built, and what each part must print
docs/experiments-matrix.md    every claim and the experiment that checks it
docs/policy.md                how one decision is computed
docs/conditions.md            the condition catalogue, one card each
docs/glue.md                  the files the job and the controller exchange
docs/write-cost.md            where the one number every deadline needs comes from
docs/b-class.md               the configured user: pricing and the bound certificate
docs/why-no-write.md          why most rounds write nothing
docs/algorithm/               what must be proven now, and how it is measured
docs/decisions/               one-page records for choices that would otherwise be lost
```

Two rules keep this from turning into a pile:

- **One subject, one page.** `docs/policy.md` says how a decision is computed; `docs/algorithm/` says
  what must be proven about it and how it is measured. Neither copies the other's tables.
- **A new page replaces something, or is a decision record.** A page that restates an existing one is
  deleted rather than merged, and a page describing work that is still in flight belongs in the work
  tracker, not in `docs/`.

Not allowed: per-feature documents, status documents, meeting notes, plans that duplicate
`README.md`, or experiment logs treated as documents. Experiment output lives in `experiments/` and
is committed only when a claim in `docs/policy.md` needs it.

If two documents describe the same thing, one of them is deleted. This rule exists so that the
document set cannot grow without a reason.

## Repository state

Remote: `bolubo/checkpoint-governor`, one branch: `main`, protected by the `verify` check. Nothing is
pushed straight to `main`: a change goes through a branch and a pull request.

**One pull request per batch, not per file.** Every push to a branch runs the whole CI (tests,
experiments, demo, image), and the run is what costs. A batch of related files therefore goes into
one branch, one commit set, one pull request, and it is squashed when `verify` is green. A pull
request that exists only to see whether one file compiles is a CI run spent on nothing.


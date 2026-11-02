# 0026 — Resumability is a verdict, not a feature

Date: 2026-11-02. Status: accepted. Follows 0025 (verify and demand) and 0017 (two numbers).

## Context

The ML stage of v0.2 arrives with the obvious temptation to become a checkpoint system: keep the shards,
compress them, repair them, restore them, and make "resume" a product feature. That path ends with us
owning the bytes, and 0025 already refused it for reasons that are stronger here: the customer's
checkpoint is large, their library is the only thing that knows what is inside it, and a restore that we
perform is a restore we are responsible for.

The alternative is narrower and more useful: say what a restore would get. A sealed copy can be
byte-perfect and still unable to continue the run — weights only, a world size the format cannot re-shard,
an optimizer the loader was told to skip. Those are the cases operators discover hours into a wasted
night, and none of them are visible from the bytes.

## Decision

**Resumability is a verdict about a declaration, and adoption is a verdict about a job.** Neither is a
feature we execute.

1. **The writer declares, we judge.** One small file beside the manifest says what was saved, under which
   world, in which format, and what the loader will skip. Missing is our conclusion to derive, not the
   writer's claim to make: a file that states its own verdict has misunderstood the contract.
2. **The job reports what it adopted.** Three glue files (`resumed-generation`, `resumed-step`,
   `load-seconds`). Absent means it did not say, which is what a cold start looks like from outside, and
   the difference between `adopted` and `not adopted` is the difference between a resume and a restart.
3. **Only a drill earns `restorable`.** Load it and take one step, with a command the operator supplies;
   fingerprint the tree before and after, and refuse a drill that changed the evidence.
4. **The verdict never reaches the policy.** A checkpoint that cannot be resumed must not change when we
   write. Making it an input creates a loop where a bad copy suppresses the write that would replace it,
   and the loop is invisible in the numbers.
5. **The vocabulary stays closed.** Three static verdicts, four adoption verdicts, one drilled verdict,
   exit codes 0/1/2, no new condition, event, or metric. A declaration is a `schema 1` file of its own;
   the plan schema stays 2 (decision 0025's freeze is not reopened).

## Consequences

- **The claim is checkable, and the evidence is named.** Each receipt line says where it came from:
  a declaration, a job, or a drill. No line is upgraded by another.
- **Time-to-resume is two accounts.** Restart belongs to the platform; load belongs to the job; a drill
  owns both. We print only what we measured.
- **A declaration can lie, so it is treated as untrusted input**: bounded, strict, no unknown fields, and
  no execution of the `loader` it names.
- **The value is in the refusals**: `not resumable: optimizer state absent` is a sentence an operator can
  act on before a preemption, and `not adopted: step 3, newest sealed 512` is the same sentence after one.

## What this does not promise

That the weights are numerically right. A drill proves one step was taken from one checkpoint; whether
that step was better or worse is the training's own business.

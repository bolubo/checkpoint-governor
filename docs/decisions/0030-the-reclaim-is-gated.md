# 0030 — The reclaim is gated

Date: 2026-11-03. Status: accepted. Extends 0021 (fleet writes), 0023 (the eviction rule) and 0028
(the direction is a library). Replaces no record.

## Context

**FACT** (S3-A, Kueue v0.9.0, a 4-CPU `lab` queue with `preempt: LowerPriority`): an intruder with a
higher priority got its victim's `deletionTimestamp` **0.2 s** after it was created. The world never
saw the pressure first: by the time the observer could publish a shortfall, the platform had already
decided. An inference that claims lead over a seconds-level scheduler is a lie, and the measured
conjunction in 0023 cannot fire earlier than the announcement it tries to precede.

**The arriving job, unlike the scheduler, is under someone's control.** A small team submits its work.
If the submission passes through one small program, that program can wait — and that waiting is the
lead time the diagnosis cannot produce.

## Decision

**The arrival is what is gated, not the victim.** `ckptgate submit` writes one reclaim record before
the job is admitted, holds it until the victim's checkpoint is sealed (or the deadline passes), and
only then lets the platform see the job.

1. **One writer, one record.** The gate writes
   `<world>/<cluster>/reclaims/<sha1(unitId)>.json`: `state` is `held`, `released` or `uncovered`,
   with `intruder`, `needMilli`, `since` and `deadline`. It is the world's third writer — the observer
   publishes the platform, the gate publishes a held arrival — and the controller reads it as one
   small file, like a unit record.
2. **`risk.preempt` gains a second source, and no new condition.** A held reclaim is an
   **announcement**: a decision already made, written before the platform acts instead of after.
   The algorithm still sees one boolean, no clock and no arithmetic; `Announced` becomes true and
   nothing else changes.
3. **`risk.eviction-likely` reads the fleet's verdict when there is one.** A permit with
   `cause: reclaim` and `verdict: granted` is the fleet saying this unit is the one a reclaim would
   take — the same shape `permit.Verdicts` already writes (0021), with the gate as a second source.
   The measured conjunction stays for deployments without a permit, and a cause this build does not
   understand falls through to it rather than silencing the condition.
4. **Fail-open, three ways.** At the deadline the gate releases the arrival with no seal. If the gate
   dies, the record's deadline passes and the scheduler resumes. If the gate picked the wrong victim,
   the write is wasted and the platform still proceeds. A gate that can block scheduling forever
   would be worse than no gate.
5. **Only submissions that pass through the gate are covered.** A job submitted straight to Kueue
   gets the announcement it always got — a `deletionTimestamp` with no lead. The gate's page says so,
   because a limit that silently is not one is the kind of claim this project exists to avoid.

## Consequences

- **The write is asked for at a moment we chose**, so the window arithmetic still holds: the gate adds
  no time to the victim's write, it only removes the race between the request and the deletion.
- **A new small module** (`contrib/gate`, client-go, its own `go.mod`), a `submit` subcommand, and a
  `--dry-run` that prints the victim it would hold without touching the cluster.
- **A page for the one audience this is for.** Platform builders can write their own glue and will;
  the gate is for a small team that can change the command it submits with.
- **G1 and G2 are the acceptance.** G1: `held → victim sealed → released → deleted`, printed as
  `intruder waited X s · victim lost 0 steps`. G2: `kill -9` the gate during a hold, and scheduling
  resumes with the record left `uncovered`.
- **A hold is not a promise.** The victim's `sealed()` may fail, the deadline may pass, and the
  user's own grace may be too short. The receipt reports what happened; it never claims a step was
  saved.

## What this does not promise

- **Not prediction.** Nothing here reads the scheduler's mind, and the inference keeps its name and
  its meaning.
- **Not a queue.** Admission and preemption stay with the platform; the gate delays one submission.
- **Not seconds.** `deadline` is the operator's number, and a hold shorter than one write is not a
  hold.
- **Not universal coverage.** Direct submissions, and platforms that will not change their submission
  path, keep today's announcement-only behaviour.

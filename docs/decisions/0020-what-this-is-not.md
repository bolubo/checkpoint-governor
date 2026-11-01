# 0020 — What this is not: no custom resource, no scheduler, no delta yet

Date: 2026-11-01. Status: accepted.

Three things the product keeps being asked for are refused here, and one sentence about clusters is
rewritten because "we do not do multi-cluster" was wrong: the execution surface already holds several
units and reads one world per cluster.

## Context

The question comes back in the same shape every time: "you have a reconcile loop and conditions, why
not a CRD", "why not schedule units, why not delta writes, why not one view of every cluster". This
repository used to answer one of them in `docs/backlog.md` ("multi-unit scheduling needs a user to
ask for it") — that page was removed with the superseded set, so the answer currently has no home.
This record is that home.

## Decision

1. **No custom resource, no operator.** The installation is a process, a manifest and a directory
   root (`docs/decisions/0019-deployment-without-crd.md`). A CRD is added only when at least two of
   these become true: state must be shared across nodes; another system must subscribe to our
   objects; a person must edit our intent with `kubectl`.
2. **No scheduling.** Units are discovered and reconciled; they are not ordered, ranked or
   prioritised. The only cross-unit rule is one write per volume at a time, which is an I/O
   interlock, not a decision about who runs.
3. **No delta writes until a writer exists.** The contract keeps `WriteKind` (`full`/`delta`) and
   nothing else: there is no field for a chain, a base snapshot or bytes since the last full write,
   and `risk.go` always answers `WriteFull`.
4. **No fleet-wide aggregator.** One process reads one world root, or several roots named explicitly;
   `supervise --members-from=kind-a=/ckpt-a,kind-b=/ckpt-b` holds units from two clusters and each
   unit reads its own world. A view that merges clusters is a display-layer decision of its own, and
   it is not made here.

## Evidence

### FACT

The code, as it is:

```
$ grep -n 'Kind:' internal/policy/risk.go | head -1
42:			Kind:          WriteFull,

$ grep -c 'ChainLength\|BytesSinceFull\|DeltaShare\|FullWriteSeconds' internal/policy/policy.go
0

$ grep -n 'Priority' internal/supervisor/supervisor.go internal/queue/queue.go
(no output: the queue has an order, not a priority)

$ grep -n 'func ReadSummary' internal/produce/adapters/world.go
110:func ReadSummary(worldDir, cluster string) (WorldSummary, error)
```

The world has been keyed by cluster since the first reader; multi-cluster is a matter of naming
several roots, not of a second data model.

`docs/backlog.md` no longer exists on `main`: the sentence about waiting for a user was removed with
the superseded pages, which is why this record exists.

### REPORT

None. No user has asked for a CRD or a scheduler; the requests are internal ("it looks like a control
plane, so it should have one").

### ASSUMPTION

The value of a scheduler-shaped product is measured in queue fairness across many jobs. We assume our
users already run one (Kueue, Volcano, YuniKorn), and that a second one inside a checkpoint controller
is a liability. Falsified if the first three adopters ask for admission control from us instead.

## Consequences

- The refusal moves into a page someone can cite, so it stops being re-argued in conversation.
- `supervise` documents `cluster=root` as the multi-cluster form; `Config.Window` stays one number per
  deployment, so two clusters with different policies are two processes.
- Nothing is deleted: the four refusals describe a boundary, not a removal.

## What this does not promise

No cross-cluster view, no fairness between units on one volume (only the interlock), no delta
economics, and no `kubectl get` of a decision. Each has a named condition that would reopen it; a
refusal without a falsifier would be a belief.

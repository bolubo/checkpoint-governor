# 0019 — The detection bound: what the probe intervals cost

Date: 2026-11-01. Status: accepted. Follows 0017 (the two numbers) and 0018 (the first mile).

## Context

0017 promised that an announced death is paid for by one write. That promise has a second half nobody
had written down: **how long it takes to notice the announcement**. A preemption the platform
announced at T is acted on at T plus however long the probe takes to read it, and during that gap the
job is still running with a plan that says everything is fine.

The intervals are constants in the adapters: preemption 5s (`internal/produce/adapters/preempt.go`),
shutdown presence 5s (`job.go`), eviction pressure 15s (`eviction.go`). `conditions.json` can override
six thresholds and times, but **not** these.

## Decision

**The intervals stay fixed in this release, and the bound is stated instead of configured.**

The worst case for an announced death is:

```
loss  =  detection (up to one probe interval)  +  the write the plan asks for
```

For preemption that is 5s plus the measured write: on the reference mount, 2.0 GB at 1.7 GB/s is about
1.2s. A preemption therefore costs roughly six seconds of work, against a default of losing everything
since the job started.

**Amendment, 2026-11-02.** The formula above is understated for `risk.preempt`, and the record stays
as written rather than being quietly rewritten. Detection is not one probe interval: the platform
writes the announcement, the observer reads the cluster and publishes the world file (5s by default),
the round samples the probe when it is due (5s), and only then does the job act. So the number to use
is **observer interval + probe interval + scheduling + the write**, not `5s + the write`.

`docs/probes.md` §Bound is the authoritative arithmetic and lists every probe's cadence with the file
that holds it. Read the `5s` above as one of three terms. The conclusion of this record does not
change — the intervals stay fixed, and the receipt reports the observed latency — but anyone quoting
"six seconds" should quote the four-term sum instead.

**Why not a flag.** Three reasons, in order of weight:

1. **A knob invites tuning without measurement.** The interval is the only input to the detection half
   of the bound; someone who halves it has changed a promise, and there is no experiment that tells
   them what they gained.
2. **The cost is already visible.** The receipt reports the observed announcement-to-write latency, so
   an operator can see the number this decision is about without changing anything.
3. **Lowering it is not free.** Each probe is a file read on a shared volume; a fleet of jobs polling
   four times more often is a volume-side decision, not a controller-side one.

## Evidence

### FACT

- The three intervals are constants in the adapters listed above; no configuration path reaches them.
- `cd .ckpt-governor && ls` and the world file are the only reads a round performs, so the interval is
  a read frequency, not a polling loop per condition.

### REPORT

- Kubernetes preemption grace is typically 30s for a default pod; a cluster admin who shortens it is
  choosing a tighter deadline than this probe can meet.

### ASSUMPTION

- That 5s is comfortably below any grace period a user is likely to configure. If someone runs with a
  10s grace, this interval consumes half of it, and the honest answer is that the product needs a
  configurable interval — with its own record.

## Consequences

- The README's failure table gains no new promise; the detection half is now written down in
  `docs/probes.md` §bound.
- A user who needs a tighter bound gets an answer: the interval is fixed, here is the arithmetic, and
  here is the reach of the write. A user who needs it configurable gets a decision record, not a flag.
- The end-to-end test (`go test -tags=e2e ./test/e2e`) asserts that an announced preemption produces a
  write plan, so the bound cannot silently become "we noticed and did nothing".

## What this does not promise

A detection latency below one interval, or any guarantee for a death the platform never announces. An
unannounced death is the case the window exists for, and its bound is 0017's, not this record's.

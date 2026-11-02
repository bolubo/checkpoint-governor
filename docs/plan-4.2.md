# Plan — 4.2, the fault matrix (four days)

The last open item of `checklist-verify.md`, and the only one that needs real processes. Everything below
runs the **real trainer binary against real files**, not a simulation: the classifier's job is to be right
about what a killed or a full disk actually leaves behind, and a fake filesystem would only be right about
a fake.

Already in the rig (`experiments/rig/run.sh`): `kill-during-write`, `user-stop`, `disk-full`. What is
missing is not the faults — it is **running the classifier on what they leave** and recording the verdict.

## What "done" means

One command produces a table with seven rows, each a fault and the verdict it must produce, and the table
is cheap enough to run on every commit:

```
$ make exp
kill mid-shard      broken/short   -> rewrite 8 -> verified
torn tail           broken/hash    -> rewrite from scratch -> verified
disk full           cannot-fit     -> no retry storm
slow fsync          waiting        -> the deadline fires, no hang
volume unmounted    broken/expired -> waiting until the deadline, then expired
two writers         one verified generation, no spurious broken
hostile shard name  refused        -> the request is never honoured
```

## Day 1 — the harness, and two honest faults

- add `--tear` to `ckpttrainer`: write the last block of the final shard with wrong bytes (right size), so
  the tail hash fails. This is what a kill at the wrong microsecond leaves.
- add `--fsync-ms`: a real `fsync` on a real filesystem, made slow on purpose. Not a sleep standing in for
  I/O — a slow syscall, so the deadline is tested against something that is genuinely blocking.
- `experiments/rig/verify-matrix.sh`: for each scenario, run the writer, then run the classifier over what
  it left, and append `fault,verdict,detail` to a CSV.

**Done when** three rows are distinct and reproducible: `short`, `hash`, `ok`.

## Day 2 — the two hard faults

- **unmount mid-write**: a tmpfs under the trainer's directory, unmounted while it writes. Requires
  privileges; in the container it is a real `umount`, and the point is that the error shape is different
  from a kill — `EIO`/`ENODEV` rather than a signal.
- **two writers, one directory**: two trainers, same generation directory, racing to seal. The contract says
  one write per volume at a time; the rig must show what happens when a deployment ignores that.

**Done when** neither produces a `verified` verdict for a copy that is not whole, and the unmount case
never hangs: it ends in `expired` at the deadline.

## Day 3 — the matrix in the gate, and one real cluster run

- wire `verify-matrix.sh` into `make exp` so the seven rows are part of `make ci`;
- one run against the kind lab (`kueue-lab`) for the preemption path — a real Kueue eviction, the classifier
  on the result — because the fault matrix proves the classifier, and only the cluster proves the loop.

**Done when** `make ci` prints the matrix, and the cluster run ends with a verified generation after a
preemption that was actually caused by Kueue.

**What actually happened (first half done, second half not).** `make exp-verify` is in the gate: five real
processes, a real tmpfs, and the deterministic rows now fail the build when a verdict is wrong. The cluster
half did not go as planned, and the reason is worth more than the run would have been:

- `kueue-lab` came up, the controller reached 1/1, and `run.sh c1` **preempted one holder to admit the
  2000m arrival** (`taken: holder-4`, `preempted: 1`). I first reported the opposite, from a truncated
  output and a post-hoc workload listing; the retraction is in `experiments/rig/FINDINGS.md` (F10).
- more importantly, **the loop is not in the cluster at all**: there is no deploy target here, no governor
  workload, no `ckpttrainer` under Kueue. Two green halves (classifier, preemption) do not make a joined
  whole, and Day 4 must not write as if they did.

**Later: the join ran.** `experiments/cluster/join/` fills the queue with four holders, starts a writer under
the contract, and sends an intruder above it. Kueue takes the writer, and the volume afterwards holds what the
writer had sealed:

```
holders admitted: 4 (of 4)
taken after 3s: job-writer-c3df0     False
generations 18 · verified 18 · broken 0 · unchecked 0
invalid rate 0.00%
the loop closed under a real eviction
```

Three things had to be true for that to be possible, and each was wrong first: the namespace needs its own
`LocalQueue` (a `queue-name` names a local queue, not the ClusterQueue); the writer must not be applied before
the holders (it takes the quota and leaves nothing to preempt); and the **holders must not be preemptible**,
because Kueue picks the cheapest victims and took four holders instead of the one writer. A check that greps for
`verified` also passes on `verified 0` — the first version of this experiment reported success on an empty
volume.

## Day 4 — the write-up

- the CSV, the numbers, and the two that matter: **work lost per preemption** and **invalid-checkpoint rate**
  (must be zero);
- update `checklist-verify.md`, and the decision record if a row disagrees with it;
- one PR, and a short report to you: which rows hold, which needed a rule change, and what the matrix still
  cannot see.

## What this plan deliberately does not do

- **No restorable check.** Loading a checkpoint and continuing a step is the ML stage's work (0025).
- **No real storage class.** A tmpfs and a local volume are not EBS. The matrix proves the classifier's
  verdicts, not the platform's durability, and the report will say so in those words.
- **No repair-in-place.** Every rewrite starts from scratch, because a patched tear is still a tear.

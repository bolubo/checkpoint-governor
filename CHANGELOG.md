# Changelog

One entry per released tag. The prototype's history before this file is `git log`; this file starts at
the first release that makes a compatibility promise.

## v0.22.0 — 2026-11-04

The release where a higher-priority arrival can be given the time to write, and where the two
conditions that had only unit tests were measured on real machinery.

**The gate.** A job submitted straight to Kueue preempts a victim 0.2 s after the intruder is
created — measured, not inferred. `ckptgate submit` holds the submission, writes a reclaim record
for the victim, waits for the victim's checkpoint to seal, and only then creates the job (decision
0030, `docs/gate.md`). Five consecutive runs: the arrival waited 4.0-4.8 s and the victim lost **0
steps**; a `kill -9` of the gate at its deadline marked the record `uncovered` and blocked nothing,
and the same arrival submitted directly lost **6 steps**. Two bugs were found by the measurement,
not by review: the probe hashed the checkpoint directory's basename instead of the `--unit` it was
given, and the 60 s write cooldown suppressed the first write of a new reclaim event. Both are fixed,
and the held reclaim is now the second source of `risk.preempt` rather than a new condition.

**Two reports told the truth.** A writer of the generation layout (`gen-N`) no longer reads as
`no writes` while its directory fills (D2). The Python client dedupes a plan by `requestId`, not by
the generation, so the same request cannot be served twice (D1). And the plan writer no longer prints
`cannot read` for a condition that was merely absent.

**Measured on real machinery.** `host.memory-pressure` in a 2 GiB cgroup, including a real OOM
death (`oom_kill` 1/1/1, the write already on disk, job and controller alive); `store.low-space` on
a loopback ext4 volume with real fsync and ENOSPC (naive 0 KiB free, governed 4.264-4.268 MiB, both
intact); the shutdown boundary at grace 60/30/10 and a declared `T_write` (`skip no-time`, zero
writes). The rows and the commands are in `docs/plan-library.md` (P9).

**The rig aims its faults.** `make exp user-stop` no longer sometimes reports a vacuous round: it
stops the job once the baseline's copy on disk is already torn, so the loss is a fact of the state
rather than of the scheduler. The storage scenario can run on `VOLUME=loop` (a real file system) as
well as on the default 8 MiB tmpfs. `make gate` is part of CI and of `hack/prepush.sh`.

The Python package carries the same number without the `v`: `ckpt-policy 0.22.0`.

## v0.21.0 — 2026-11-02

The first release where the whole chain was run under a real eviction, and the first where the receipt's numbers
were checked against a real volume.

**The receipt now counts what a generation-layout writer (`gen-N`) produces.** Before this release the product
counted only `step-` directories, so a writer of the other layout was invisible to its own accounting: the receipt
could say `no writes` while the checkpoint directory filled up. A customer reading that sentence may well go and
delete the directory themselves — which is why this is the first thing in these notes. Two tests now pin it.

**Under a real preemption** (`experiments/cluster/join/run.sh`, reproducible and self-cleaning):

```
holders admitted: 4 (of 4)
taken after 3s: job-writer-c3df0     False
generations 18 · verified 18 · broken 0 · unchecked 0
invalid rate 0.00%
```

**Disk.** The sweep is bounded in bytes and runs every round, so a job that is preempted and then stops writing
no longer leaves a copy in `.trash` forever; a round's sweep must be able to give back what that round retired
(without it the trash grew at 31 copies); retention tightens under space pressure; the ledger's one unbounded map
is capped. A full volume is now a test: three copies survive and verify, the fourth fails, the writer exits
non-zero.

**Building this repository.** `hack/pr-check.sh` builds the tree a PR will be, not the working directory — CI
compiles *main plus the PR's files*, and four pull requests went red today because a declaration or a test was
missing from that list. `hack/footprint.sh` reports what a machine has spent on experiments and fails over
budget; it reports rather than deletes.

## v0.15.0 — 2026-11-02

The release a stranger can install, and whose promises are tested. The decision core does not change;
what is new is the surface around it. Scope and acceptance lines: `docs/product-v0.15.md`.

**Install.** `curl -fsSL …/install.sh | sh` downloads the tarball for your architecture, checks it
against `SHA256SUMS`, and only then puts `ckptctl` on your PATH. `make install-smoke` proves that path
offline against a locally built `dist/`, and CI runs it on every push.

**Compatibility.** The plan schema is a range: `doctor` prints which schema wrote a plan and refuses
one this build cannot read, with exit code 2. A directory written by the previous build is checked in
as a fixture, and a run on it continues its ledger instead of starting over. The names a scraper or a
script keys on — six metrics, nine event kinds, fourteen conditions, the exit codes, the flags — are
pinned by `TestFrozenNames`, and `docs/compatibility.md` says what is not promised.

**Proof.** `make spot` preempts a real job in ten seconds: one write, one kept checkpoint, the cause
and the cost printed as a receipt.

**Docs.** `docs/install.md` names the three shapes and the files each side writes; `deploy/` gains
copyable `conditions.json` and `deployment.json` beside the manifest.

`make ci` and `hack/prepush.sh` run the same gate: `check`, `e2e`, `exp`, `demo`, `install-smoke`,
`spot`.

Not in this release: the training-script callback and the resume contract. They are v0.2, and are
named there so they do not leak into this one.

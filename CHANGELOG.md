# Changelog

One entry per released tag. The prototype's history before this file is `git log`; this file starts at
the first release that makes a compatibility promise.

## v0.28.0 — 2026-11-05

The release that makes the contract easy to see, machine readable, and available to an agent.

**`ckptctl demo`.** A real two-rank generation, one real fault (`kill`, `torn`, `short` or `clean`),
and the same `resumable`/`verify` answers a production copy gets, in thirty seconds without a cluster
or a GPU. `--repair` seals it again; `--json` is the same report. The demo refuses a directory it did
not create, and `--clean` refuses without its marker.

**`ckptctl setup`.** `--local` measures the volume and prints the four lines of glue; `--check` reads
the cluster (kubectl only, read-only) and prints kubectl, Kueue, capacity, volume, storage, gate and
controller as `ok`/`blocked`/`unknown` with a remedy; `--write` emits a governed Job, a gate wrapper,
an MCP registration and a README. It applies nothing.

**A read-only MCP server.** `ckptctl mcp --root=<dir>` answers `status`, `resumable`, `verify`,
`explain`, `storage` and `evidence` over stdio. Every path is bounded by the root; an escape, including
through a symlink, is refused. `setup --agent=claude|codex --apply` writes the registration and the
skill with a backup, idempotently.

**One evidence bundle.** `ckptctl evidence` and `--json` on `explain`, `audit`, `verify` and `doctor`
share schema 1: status, resume, newest copy and its shards, attempts, storage, and a closed set of
problem codes. The bundle assembles, it does not re-judge.

**Tests.** `make py` runs 60 tests; `make test-claims` prints
`claims: 51 · tests found: 51 · missing: 0`; `make mcp-test` covers the handshake, the six tools, the
path guard and the config writers. The two-pod gate harness still prints
`sealed both ranks · arrival created · victim lost 0`.

## v0.27.0 — 2026-11-05

The release where the volume is measured instead of assumed, and a multi-pod victim is held as one
workload.

**`ckptctl doctor --storage <dir>`.** It writes, syncs, renames, syncs the directory, and reads the
result from a second process, then prints `local` / `shared-safe` / `shared-unsafe` with the promise
each tier makes (process death / node death / volume death). The exit code is the verdict. On one
machine a local `tmpfs` is `local` and WSL's 9p mount is `shared-safe`; every failed line names its
reason. The contract is `docs/storage.md`.

**A seal reads the disk back.** Python `Governor.sealed` and `ckpttrainer --contract` re-open every
shard before writing the manifest — size and declared tail hash, one retry — and refuse to seal a
shard they cannot read. The failure is recorded in `.ckpt-governor/seal-failed`, and `resumable` no
longer answers `world N/N` for a generation that was never sealed. The Go writer's tear fixture is
injected after the readback, so the fault matrix still produces a damaged copy to judge.

**The gate releases on a verified copy.** `ckptgate` checks the newest generation with the same
verifier before releasing an arrival: shards present and readable, `sealedAt` not older than the hold,
and one shard per rank of the workload. A missing shard keeps the arrival held and prints the rank;
the deadline is fail-open and marks the record `uncovered`.

**One workload, one hold.** The observer records each pod's owning workload; the gate writes one
reclaim record per workload, naming its pods, and the controller finds it through the unit's
`workload` field. A two-pod Job produces one hold, not two. A multi-pod victim on a per-node
`hostPath` (or whose pods see different roots) is refused with the reason.

**Shared-volume progress throttle.** `Governor(storage="shared")` writes the step cell at most once
per second (configurable), flushing the last step on `sealed`, `ending` and `close`.

**Measured.** `hack/kind-multipod-gate.sh` on kueue-lab runs a two-pod indexed Job with the real,
torch-free Python client and prints `sealed both ranks · arrival created · victim lost 0`.

**Tests.** `make py` runs 60 tests; `make test-claims` prints
`claims: 44 · tests found: 44 · missing: 0`.

## v0.26.0 — 2026-11-05

The release where a `pip install` is the whole install, a multi-rank answer checks the disk, and the
per-step tax is gone.

**The command comes with the wheel.** `pip install ckpt-policy` installs `ckptctl` into the venv, so
`ckptctl status <dir>` and `ckptctl resumable --dir` work without `install.sh`. `ckpt_policy.__version__`
is read from the installed distribution instead of a hard-coded string. 0.25.0 predated the command;
0.26.0 is the first release that carries it.

**A deleted shard is not `world 2/2`.** `resumable` stats every rank shard the manifest marks. A
two-rank copy with `rank1.pt` removed answers `not resumable: rank 1 shard missing`; restoring the file
answers `resumable (gen 1, step 300, world 2/2, full state)`.

**Status reads the directory, not just the last ledger round.** A framework checkpoint is shown as
`newest sealed step 1000`, not `none`; a job that ended after the last round no longer prints
`phase writing` forever. `status` still takes the directory as an argument.

**`torchrun` supplies the topology.** `Governor` reads `RANK` and `WORLD_SIZE`, and only rank 0 starts
the engine. The multi-rank glue loses four lines, and a non-leader cannot start a second controller on
the same plan.

**The progress cell stops paying for fsync.** `step()` keeps the atomic rename but skips the fsync:
the cell is liveness, not data. On the S1 job (1000 steps, local ext4) the governed run fell from
**16.8s to 6.1s**, against a 5.9s control; the two-rank 300-step run fell from **15.2s / 13.8s to
5.0s / 4.9s**. Manifests, declarations and the plan still fsync.

**Ready-made glue.** `contrib/ckpt-policy/examples/hf_trainer.py` and `two_ranks.py` are complete,
runnable versions of the single-card and multi-rank paths; `docs/multi-rank.md` explains who starts the
engine, where shards go, and who seals. `docs/first-ten-minutes.md` and `docs/troubleshooting.md` start
from the commands and the messages a user actually sees.

**Tests.** Thirty-seven outbound promises name the test that would fail if they stopped being true
(`make test-claims` prints `claims: 37 · tests found: 37 · missing: 0`); `make py` runs 54 tests.

**Not in this release.** DDP/FSDP model sync on a real multi-GPU host, a real multi-pod Kueue victim
(the S3 harness uses `ckpttrainer` on one pod), Lightning/Composer examples, and macOS/Windows wheels.

## v0.25.0 — 2026-11-05

The release where the install is copyable, the framework runs are real, and the declaration says what
it needs.

**A fresh machine can follow the page.** `docs/install-python.md` and all three integration pages
start with `python3 -m venv .venv`, because a system Python managed by PEP 668 refuses `pip install`
— measured on this machine, not assumed. Each integration page opens with one `Paste this` block whose
first import is identical everywhere.

**Disk is priced before the first write.** `ckptctl init --params=7B` estimates 18 bytes per
parameter (fp16 weights, two Adam moments, gradient and master weights) and prints a `disk:` line
for every answer. The number always says `estimated … not measured`, and the help says it is about
disk, not whether this GPU can train the model.

**The declaration says what it needs, and the wrapper stops retyping it.** `ckptctl declare` gained
`--world`, `--tp`, `--pp`, `--dp` and `--reshardable`, so a multi-rank copy is judged against the
world it was written under. `ckptctl wrap` reads `deployment.json`'s `grace_seconds` when `--grace` is
absent and prints where the number came from. `ckptctl status` names the `ext` keys a reader ignored
(names only, never values).

**A receipt says where the copy lives.** `durability      local disk: a copy outlives the process,
not the volume` is printed whether or not the run wrote anything.

**Real Lightning and Composer runs found real defects, and they are fixed.** Both callbacks were
plain objects: Lightning failed at `setup`, Composer at `run_event`. Both drove public save
functions that write a file but skip the framework's own bookkeeping — a real run left
`best_model_path` empty. They now subclass the framework callback and drive the framework's own
routines (`_save_topk_checkpoint`/`_save_last_checkpoint`, `CheckpointSaver._save_checkpoint` and
`_rotate_checkpoints`). A framework write leaves no `gen-*`, so requests are now deduped by
`requestId`; dedupe by generation used to silence every window request after the first one.

**Version skew with real binaries.** `hack/skew.sh` checks out the ref that shipped the 0.24 reader,
builds it, and lets it refuse `min_reader: 0.25` on its own terms; this build accepts the same file.

**Tests.** Thirty-three outbound promises name the test that would fail if they stopped being true
(`make test-claims` prints `claims: 33 · tests found: 33 · missing: 0`).

**Not in this release.** Physical-disk low-space, the 1-second grace matrix, a two-node kind
cluster, real-model resume (T-6), and long soak runs — the testing round owns those.

The Python package carries the same number without the `v`: `ckpt-policy 0.25.0`.

## v0.24.0 — 2026-11-04

The release where "can this continue" is answered for the layout your framework already writes.

**The finder reads more than `gen-*`.** One package (`internal/layout`) recognises `gen-N/`,
`step-N/`, Hugging Face's `checkpoint-N/`, Lightning's `*.ckpt`, a Composer episode, and whatever a
user's `.ckpt-governor/layout.json` names. Preference is fixed (our generation first, then the highest
step; at the same step `checkpoint-*` > `*.ckpt` > Composer), and a half-written directory is reported
as `unknown: … is not sealed` instead of being counted as the newest copy. `resumable`, `status`, the
receipt and the loop all ask the same finder; the receipt and `status` can no longer print two
different numbers.

**Declarations say what the job needs.** `resume.json` gained optional `requires` (default five; a
framework declares four, because it re-iterates the dataset instead of saving a data position),
`target` (`"."` or a sibling artifact), `caveats` (`data-position-not-saved`), `min_reader`, and `ext`.
An older declaration keeps its exact sentence; a newer one is refused with the upgrade named.

**Every adapter declares.** Hugging Face writes `checkpoint-<step>/resume.json`, Lightning writes
`<file>.ckpt.resume.json`, Composer's rank 0 writes `<artifact>.resume.json`. `ckptctl declare` is the
manual half, and a `probe_cmd` is the last glue (5 s, then `unknown: probe_cmd failed`).

**`ckptctl wrap` makes the stop handshake executable.** It takes the first signal, writes `stopping`,
waits for a sealed copy or the deadline, then forwards SIGTERM to the process group. Every branch
prints, including `final write NOT made`. Proven at grace 60/30/10, N=3; a second signal forwards at
once and records `release-forced`.

**Multi-rank ownership.** Each rank owns `ranks/<rank>/step`; only rank 0 writes team-level files
(`lead-only file` otherwise). Progress is the slowest rank, `status` prints `members 2/2 · spread 4`,
silent ranks are named, and a missing rank shard is `not resumable: rank N shard missing`. Retention
only removes our own layouts.

**Tests.** Thirty outbound promises now name the test that would fail if they stopped being true
(`make test-claims` prints `claims: 30 · tests found: 30 · missing: 0`). New failure injections: a
half-written directory, a copy with no declaration, a declaration from a newer build, a probe that
never answers. `hack/wrap-grace.sh` and `hack/members.sh` are real-process proofs.

**Not in this release.** Built-in format sniffing (`probe_cmd` instead), cross-node coordination in
`wrap`, rank barriers, real Lightning/Composer runs (v0.25), macOS/Windows wheels (v1).

The Python package carries the same number without the `v`: `ckpt-policy 0.24.0`.

## v0.23.0 — 2026-11-04

The release where a hold covers the nodes a multi-pod arrival actually needs.

**A `parallelism: 2` Job is two pods, not one.** The gate used to read one pod's request, pick one
node, and drop every unit on any other node. Room freed on worker-1 cannot hold a pod that must land
on worker-2, so the second node's victim still died with the 0.2 s notice the gate exists to remove.
The probe printed exactly that: one node picked, one victim, the second node never asked.

**Now it is per node.** `podsOf` reads `spec.parallelism`; the arrival's total is `perPod × pods`;
`pickNodes` spreads the pods over the emptiest readable nodes (cordoned and unmeasurable nodes
skipped, evenly shared when there are fewer nodes than pods). The shortfall is per node, and victims
are chosen per node with the fleet's rule (lowest priority, then the largest footprint; a tie group
is never broken). The queue is a gate, not the arithmetic (0023): the victims together must free
`max(queue gap, Σ node gaps)`, and the queue must allow preemption in the first place. A hold is
**all-or-nothing** — one short node with no governed unit below the arrival means no hold at all.

`--node` takes a comma-separated list. A single-pod, single-node arrival keeps the same arithmetic
and the same first line as before (pinned by a test).

**Tests.** Seven unit tests and two end-to-end tests against a fake client: a two-node world, two
governed directories, two stand-in controllers that write their manifest when they see the reclaim —
one victim per node, both seals observed before the job is created, reclaims released, permits
granted; and a refusal when the second node has nobody to ask.

**Limits written down** (decision 0031, `docs/gate.md`): one pod per node is a model of how multi-pod
Jobs are placed, not a scheduler promise; only CPU enters the arithmetic; only a `batch/v1 Job` is
read (PyTorchJob/MPIJob are not); a real cross-node hold needs a checkpoint volume that both the gate
and the pods can see — the test cluster's `hostPath /g1` is per node, so that run stays a deployment
prerequisite.

The Python package carries the same number without the `v`: `ckpt-policy 0.23.0`.

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
what is new is the surface around it. Scope and acceptance lines are in this entry.

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

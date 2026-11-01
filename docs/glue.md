# The glue: what the job writes, and what reads it

Status: contract between the training job and this controller. Date: 2026-11-01. Everything here
lives in `<checkpointDir>/.ckpt-governor/`. A job that writes nothing here still works: it is governed
by the job-local facts (there is a checkpoint, a write is in flight, the copies are intact), and it
is never written unless the user signed a fallback. Writing these files is how a job becomes visible.

| File | Format | Written by | Read by | Meaning when absent |
|---|---|---|---|---|
| `boot` | base36 unix nanoseconds | the job, once at start | `observe` | the unit's generation cannot be tied to a boot; a restart looks like the same unit |
| `step` | decimal integer | the job, every step | `observe` → `Snapshot.Step` | the step is taken from the newest sealed checkpoint, which is a floor |
| `progress` | float in [0,1] | the job, while a write runs | `observe` → `Snapshot.SaveProgress` | the write's progress is unknown (`-1`) |
| `ending` | presence only | the job, when training is finishing | producer `job.ending` | training is not ending |
| `stopping` | presence only | the job, on SIGTERM, before its grace period starts | producer `risk.shutdown`; the loop's exit watch | the job was not told to stop |
| `expected-bytes` | decimal integer, bytes | the job, once it knows its own size | `loop`: the space rule before the first seal, and the estimate | the size is unknown until a checkpoint seals |
| `last-save-seconds` | float, seconds | the job, from its own previous save | `loop`: the estimate of one write | the estimate falls back to size over probed throughput |
| `seconds-left` | integer, seconds | the job, while it is stopping | `loop`: the deadline door; the `risk.shutdown` producer | nobody announced a deadline |
| `may-exit` | presence only | **the controller**, when it has observed that no write is in flight (or that the write's deadline passed, in which case it reports the loss) | the job, `ckptctl glue` | the final write has not landed; it is not safe to kill yet |
| `members` | one rank per line | whoever starts the gang | `unit` membership | the unit has one member |
| `gpu` | float, percent busy | the job's own probe | producer `host.gpu-hot` | the GPU condition is absent (no GPU, or no probe) |
| `step-baseline`, `step-rate` | integer, milliseconds | the job, at start and on a step that wrote nothing | producer `job.step-slow` | no baseline, or no measurement yet; the condition is absent |
| `write-baseline`, `write-rate` | integer, milliseconds | the job, at start and after each write | producer `store.slow` | no baseline, or no measurement yet; the condition is absent |
| `thresholds.json` | the effective configuration | the controller, once at start | `ckptctl explain`, `ckptctl config` | the run predates the record; explanations fall back to the file, then the catalogue's numbers |
| `deployment.json` | operator declaration: `world.dir`, `capabilities` | a person, once per deployment | the controller's capability gate, `ckptctl doctor`, `ckptctl config` | nothing is declared: every upstream is assumed present, which is the safe default |
| `world/<cluster>/summary.json` | the cluster: nodes, capabilities | the observer, every few seconds | `risk.eviction-likely`, `risk.preempt`, `ckptctl world`, `status`, `explain` | no observer on this deployment: both world conditions are absent |
| `world/<cluster>/units/<sha1>.json` | this unit's own record: node, priority, QoS, deletion timestamp | the observer | the projection behind both world conditions | the observer does not track units: the conditions are absent, not unknown |
| `state.json` | the plan (see `design-v1.md`) | the controller | the job | there is no plan; the job must fall back to its own timer |

## The rules that make these safe to write

- **Every file is written by rename.** A reader must never see half a step number or half a plan.
- **A file that is not written is not an error.** Each row above says what its absence means, and in
  every case the answer is "unknown" or "absent", never zero.
- **The controller writes exactly one *plan* here**: `state.json`, plus its own bookkeeping
  (`internal.json`, `attempts.json`, `lease`, `events.jsonl`, `thresholds.json`). Everything else
  belongs to the job — and the glue files have one reader, `adapters.ReadGlue`, so the CLI and the
  executor cannot drift into disagreeing about what a missing file means.
- **The plan carries both numbers and a request identity.** `baselineSeconds`/`baselineNone` is the
  user's `no-us` — what the job does when the plan has expired; `windowSeconds` is his `with-us`;
  `requestId` names the one write being asked for, so the job acts at most once per request. `expiresAt`
  means only "a live controller exists" (a few rounds), never a period.
- **A stop has two phases, and only the second one means "kill me".** `stopping` says the job was
  asked to stop; `may-exit` says the final write is on disk. A controller that treats the first as
  the second loses exactly the checkpoint the stop was for. The second half is the controller's to
  write, because it is the side that can *see* whether a write is still in flight — asking the job to
  report what the controller can observe is the mistake this handshake exists to avoid.
- **The numbers the controller reads are not conditions.** `expected-bytes`, `last-save-seconds` and
  `seconds-left` never reach the algorithm: they are what the executor needs to answer "can a write
  finish", and the algorithm stays boolean and clock-free.
- **`ending` is the strongest signal a job can give.** It says "the work that exists now is the
  work that matters"; it is what makes a final checkpoint happen without a timer.
- **The job keeps running until we answer.** `ending` and `stopping` are requests, not exits: the job
  writes its final checkpoint, then waits for `may-exit`, bounded by its own grace period, and records
  `release-timeout` if nobody answers — an unanswered handshake leaves a trace instead of losing the
  last write silently. The end of a run is the one write that does not depend on any configuration
  (`no-us none` included): with no live plan the job keeps that promise itself and says so.

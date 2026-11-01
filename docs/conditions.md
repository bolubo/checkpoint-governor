# Conditions: one card per row, and who produces it

Status: implementation note for decision 0013. The catalogue itself lives in
`internal/policy/condition.go` and is frozen there; this page says how each row becomes a reading.

Two tests keep this page honest, and both read it from disk:

- `TestEveryCatalogueConditionHasAProducer` — every catalogue row has a producer, or is declared
  absent, or is derived by the sampler.
- `TestEveryCatalogueRowHasACard` — every catalogue row has a card below with all nine answers, and
  every card names a row that exists. A new condition without a card fails the build.

## The ten questions

Every card answers the same questions in the same order, so "how is this 0/1" is answerable by anyone
without reading the code: what a 1 means, where the signal comes from, the exact definition, the
cadence, the cost, what absent means, what unknown means, which catalogue row consumes it, and which
test pins it.

## `watch.broken`

- **what 1 means**: the machinery that reads conditions is dead, so we are blind
- **source**: the sampler's own read outcomes, not the world
- **definition**: three consecutive unreadable reads, or a clock that jumped backwards
- **cadence**: every round
- **cost**: free
- **absent means**: never — it is a statement about us
- **unknown means**: not applicable; this *is* the unknown state, collapsed into one condition
- **consumer**: catalogue row (urgent, missing=inert)
- **test**: `internal/produce/sampler_test.go`

## `risk.preempt`

- **what 1 means**: the platform has decided to take this unit (deletion timestamp or eviction event)
- **source**: the world projection — this unit's record in `<worldDir>/<cluster>/units/`
- **definition**: `deletionTimestamp` is set, or `evicted` is true. An **announcement**: no inference, no ratio, no guessing
- **cadence**: 5s
- **cost**: cheap (one small file)
- **absent means**: no observer at all, an observer that does not track units, or `preemption.exists=false` in `deployment.json` — the deployment does not have the condition, and `doctor` says which of the three
- **unknown means**: the observer tracks units and has lost ours, or the summary is past its staleness limit; urgent, because an announcement we cannot read is not calm
- **consumer**: catalogue row (urgent, missing=urgent), earlier than the inference so an announcement always wins
- **fanout**: `one-per-victim` (0021). A reclaim takes one or a few units out of many, so a unit that
  fires writes only if the observer's verdict names it; the rest publish
  `skip · this unit is not the reclaim target`
- **test**: `TestPreemptionFiresOnAnAnnouncement`, `TestPreemptionIsAbsentWithoutAWayToHear`

## `risk.shutdown`

- **what 1 means**: the job has been told to stop and has a grace period to spend
- **source**: `.ckpt-governor/stopping`, written by the job on SIGTERM
- **definition**: the file exists
- **cadence**: 5s
- **cost**: cheap (one stat)
- **absent means**: never — the file's absence is the reading 0, not a gap
- **unknown means**: the file exists but cannot be stat'ed; urgent
- **consumer**: catalogue row (urgent, missing=urgent)
- **test**: `TestStopWritesTheStoppingFile`, `TestRiskShutdownTurnsAStopIntoASave`

## `store.low-space`

- **what 1 means**: the next write may be the last that can complete
- **source**: `statfs` of the checkpoint volume
- **definition**: free < factor × one checkpoint; factor default 2, because the old and the new copy coexist
- **cadence**: 5s
- **cost**: cheap
- **absent means**: never — a local volume always exists to measure
- **unknown means**: `statfs` failed; urgent, because we cannot see the room
- **consumer**: catalogue row (urgent, missing=urgent)
- **test**: `TestLowSpaceFiresBelowTwiceTheCheckpoint`

## `host.memory-pressure`

- **what 1 means**: the kernel is about to kill this job for memory
- **source**: cgroup v2 `memory.current` and `memory.max`
- **definition**: used/limit ≥ 0.95; 0.90 is a warning, not a condition
- **cadence**: 10s
- **cost**: cheap (two reads)
- **absent means**: no cgroup memory limit — a bare-metal job has no line to be near
- **unknown means**: the files exist but cannot be read; urgent
- **consumer**: catalogue row (urgent, missing=urgent)
- **test**: `TestMemoryPressureFiresAtTheEvictionLine`

## `host.gpu-fault`

- **what 1 means**: a GPU is failing (Xid, ECC growth, fell off the bus)
- **source**: none on this build
- **definition**: n/a — no GPU
- **cadence**: n/a
- **cost**: free
- **absent means**: always, on this build
- **unknown means**: would be urgent
- **consumer**: catalogue row (urgent, missing=urgent)
- **test**: `TestAbsentConditionsSayTheyAreAbsent`

## `job.ending`

- **what 1 means**: training is finishing, so the last write is the useful one
- **source**: `.ckpt-governor/ending`
- **definition**: the file exists
- **cadence**: 10s
- **cost**: cheap
- **absent means**: never — absence is the reading 0
- **unknown means**: the file cannot be stat'ed; inert, because the next timer covers it
- **consumer**: catalogue row (urgent, missing=inert)
- **test**: `TestEndingReadsTheFileAsTheSignal`

## `risk.eviction-likely`

- **what 1 means**: the node is likely to need this job's room soon
- **source**: the world projection — `<worldDir>/<cluster>/summary.json` plus this unit's record, both written by a platform observer
- **definition**: CPU or memory requests ≥ 90% of allocatable, or ≥ 1 pending high-priority pod, or a cordon; the line is deployment config
- **cadence**: 15s
- **cost**: cheap (one summary + one unit record)
- **absent means**: no observer, an observer that does not track units, `eviction.exists=false`, or `--infer-eviction=off`
- **unknown means**: the observer tracks units and lost ours, the summary is past its staleness limit, or no ratio can be read (including a node whose pods declare no requests); urgent, because a blind inference is not a calm node
- **consumer**: catalogue row (urgent, missing=urgent), after every announcement
- **fanout**: `one-per-victim` (0021), for the same reason as the announcement: this predicts the same
  event, and that event takes one unit out of many. A fleet where every unit infers "maybe me" writes
  M times for one death
- **test**: `TestEvictionFiresWhenTheNodeIsFillingUp`, `TestEvictionIsUnknownWhenRequestsWereNeverDeclared`

## `store.blocked`

- **what 1 means**: the write target refuses right now, so a write cannot help
- **source**: create and remove a temp file in the checkpoint directory itself. It used to probe `.ckpt-governor`, which made the condition unreachable: an unusable governor directory stops the controller before any condition is read.
- **definition**: any error other than "the directory does not exist yet"
- **cadence**: 5s
- **cost**: cheap (two syscalls)
- **absent means**: never
- **unknown means**: n/a — a refusal is an answer, not blindness
- **consumer**: catalogue row (warn, missing=inert)
- **test**: `TestBlockedMapsTheProbeAnswer`

## `store.slow`

- **what 1 means**: writes are much slower than this deployment's normal
- **source**: `.ckpt-governor/write-rate` against `.ckpt-governor/write-baseline`, both written by the job
- **definition**: current ≥ 2 × baseline; the factor is deployment config
- **cadence**: 10s
- **cost**: cheap (two reads)
- **absent means**: the job never calibrated — there is no normal to compare against
- **unknown means**: a file is not a number; inert, because a diagnostic cannot cost us work
- **consumer**: catalogue row (warn, missing=inert)
- **test**: `TestSlowFiresAtTwiceTheJobsOwnBaseline`

## `host.gpu-hot`

- **what 1 means**: the GPU is throttling
- **source**: none on this build
- **definition**: n/a
- **cadence**: n/a
- **cost**: free
- **absent means**: always, on this build
- **unknown means**: inert
- **consumer**: catalogue row (note, missing=inert)
- **test**: `TestAbsentConditionsSayTheyAreAbsent`

## `host.cpu-throttled`

- **what 1 means**: the CPU quota is throttling the job
- **source**: cgroup v2 `cpu.stat` (`throttled_usec`, `usage_usec`)
- **definition**: throttled/used ≥ 0.20, computed from cumulative counters so one reading is enough
- **cadence**: 10s
- **cost**: cheap
- **absent means**: no cpu controller — a job with no quota cannot be throttled by one
- **unknown means**: `cpu.stat` unreadable; inert
- **consumer**: catalogue row (note, missing=inert)
- **test**: `TestCPUThrottledIsARatioOfTheQuotaLost`

## `job.step-slow`

- **what 1 means**: a training step takes much longer than this job's normal
- **source**: `.ckpt-governor/step-rate` against `.ckpt-governor/step-baseline`
- **definition**: current ≥ 2 × baseline; steps that wrote a checkpoint are excluded, because a write legitimately slows the loop
- **cadence**: 10s
- **cost**: cheap
- **absent means**: the job never calibrated
- **unknown means**: a file is not a number; inert
- **consumer**: catalogue row (note, missing=inert)
- **test**: `TestSlowFiresAtTwiceTheJobsOwnBaseline`

## `net.long-outage`

- **what 1 means**: the network has been down long enough to matter
- **source**: none on this build
- **definition**: n/a — no remote store
- **cadence**: n/a
- **cost**: free
- **absent means**: always, on this build
- **unknown means**: inert
- **consumer**: catalogue row (note, missing=inert)
- **test**: `TestAbsentConditionsSayTheyAreAbsent`

## Three decisions worth remembering

- **Absent is declared, not omitted.** A condition this build cannot have is still a row in
  `adapters.All()`, so `ckptctl doctor` can print "this environment does not have it" instead of
  leaving a hole that looks like a calm job.
- **`store.corrupt` is not a condition.** The policy repairs a broken copy from the typed `Copies`
  fact before it ever looks at the catalogue; a second path through `Evidence` would say the same
  thing twice and would have to read a checkpoint every cadence to say it.
- **The two "slow" conditions compare the job with itself.** Baseline and current rate are both
  written by the job, in the same unit. The controller never decides what fast means for a workload
  it cannot see.

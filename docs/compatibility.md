# Compatibility: what a version promises

The product's contract is not an API; it is a set of names and files that other things read. A job
reads the plan, a scraper reads the metrics, a script reads the exit code, a log shipper reads the
event kinds, an operator filter reads the condition names. Those readers are the reason a rename is a
decision and not a cleanup.

## The four rules

1. **One plan schema at a time, and it says which one.** `.ckpt-governor/state.json` carries `schema`;
   `ckptctl doctor` prints `plan schema 2, this binary reads 2`. A plan outside the read range is
   refused with its number named, and `doctor` exits non-zero — a table of environment facts must not
   read as a verdict about a directory this build cannot govern.
2. **Fields are additive within a schema.** Adding a field is free; removing or renaming one changes
   the plan schema, and the change comes with a note here. (This is why the schema number exists: a
   reader that knows the number knows which contract it holds.)
3. **Names are frozen.** Condition names, event kinds, metric names and exit codes are promises. Adding
   one is allowed; changing one is a breaking change. `TestFrozenNames` pins the lists, so a rename
   fails the build before it reaches a reader.
4. **Old directories continue, they do not restart.** A directory written by the previous build is
   adopted as history: its ledger is read, its unit and generation are kept, and its plan is not
   refused. The epoch advances because a new controller is a new epoch — that is the crash rule, not a
   reset.

Code cites decisions **by number** (`decision 0013`), never by page path; a page may link a path. The
index that resolves a number is `docs/decisions/README.md`, and every number cited in code is checked
against it (`TestCommentsCiteDecisionsNotPagePaths`).

## The frozen lists

**Plan**: schema `2`; a file outside this range is refused. The fields are the plan a job acts on
(action, reason, request id, both numbers, copies, kind, retention, why, missing, reasons,
sensitivity) plus the epoch and expiry that make it authoritative.

**Conditions** (precedence order, decision 0013): `watch.broken`, `risk.preempt`, `risk.shutdown`,
`store.low-space`, `host.memory-pressure`, `host.gpu-fault`, `job.ending`, `risk.eviction-likely`,
`store.blocked`, `store.slow`, `host.gpu-hot`, `host.cpu-throttled`, `job.step-slow`, `net.long-outage`.

**Event kinds** (`events.jsonl`): `decision`, `publish`, `attempt_start`, `attempt_end`, `condition`,
`lease`, `backoff`, `error`, `config`. The `code` field carries the finer vocabulary (for example
`notify-failed`); codes are additive, existing ones keep their meaning.

**Metrics** (`serve --metrics`): `checkpoint_staleness_seconds`, `checkpoint_over_bound_seconds`,
`checkpoint_blind_conditions`, `checkpoint_writes_total`, `checkpoint_units`, `checkpoint_clusters`.
Labels are `unit` and `cluster`; `checkpoint_writes_total` adds `kind`. An unknown staleness is `+Inf`,
never a zero that would read as a fresh checkpoint.

**Exit codes**: `0` the command answered, `1` it ran and failed, `2` it was not asked properly or
refused to answer. A script may branch on these three.

**Flags**: the authoritative list is `ckptctl <command> --help`. The ones a deployment script may rely
on are `run --dir --unit --no-us --window --every --once --dry-run --webhook`, `supervise --members
--members-from --rounds --forever --every --webhook`, `serve --addr --members --members-from`,
`doctor --dir`, `wrap --dir --grace --teardown`, and `declare --dir --step --requires --state
--format`. Adding a flag is free; removing or renaming one is breaking.

## What this page does not promise

No stability for `internal/` packages (they are not importable on purpose), no stability for the
`debug` output of `--verbose`, and no cross-version readers: this build reads schema 2 only. The day it
reads two schemas, `Reads()` becomes `2..3` and this table grows a row.

**A countdown is refreshed by the job's own steps.** `seconds-left` is written when a deadline is
announced and on every `step()`; a step that runs longer than the grace period leaves the cell stale,
and no reader checks its age. The gate therefore decides on the last number the job wrote. This is
recorded here rather than fixed: at the time of the archive, the two honest repairs were a timer thread
in a library that promises no background work, or changing the meaning of a frozen cell name.

## Added in v0.2, and additive by construction

The current number is `v0.29.0`; v0.27, v0.26, v0.25, v0.24, v0.23, v0.22, v0.21, v0.2 and v0.15 are
earlier numbers (decision 0029). Their additions are carried by every later release.

The resume contract adds files; it renames nothing and adds no frozen name (decision 0026).

- **`resume.json`, schema 1**, beside a generation's manifest: `generation`, `step`, `format`,
  `world{ranks,tp,pp,dp,reshardable}`, `state{model,optimizer,scheduler,rng,dataloader,scaler,ema}`,
  `partial`, `loader`. Unknown fields are refused; 64 KiB bound; one JSON value per file.
- **Three job glue files**: `resumed-generation`, `resumed-step`, `load-seconds`. Absent means the job
  did not say, which is what a cold start looks like; a job that writes none is governed exactly as
  before.
- **Three commands**: `resumable`, `resumed`, `drill`, with the same three exit codes as everything else
  (`0` yes, `1` checked and no, `2` cannot decide).
- **Not added**: no condition, no event kind, no metric, no plan-schema move. `TestFrozenNames` is
  unchanged and still prints `frozen: 14 conditions, 9 event kinds, 6 metrics, 2 exit codes`.

## Added in v0.24, additive

The plan schema stays 2 and `resume.json` stays schema 1. Everything here is an optional field or a
new name; no existing name changed meaning.

- **Five optional declaration fields**: `requires` (the states this job needs; absent means the
  original five), `target` (`"."` or a sibling file name; absent means `"."`), `caveats`
  (`data-position-not-saved`), `min_reader` (a declaration from a newer build is refused with the
  upgrade named, never misread), and `ext` (readers ignore its contents; every other unknown field is
  still refused). A declaration written before these existed produces the sentence it always did.
- **More layouts are found, none renamed**: `step-N/`, `checkpoint-N/`, `*.ckpt` and a Composer
  episode are read by the same finder as `gen-N/`. `gen-*` keeps priority; a half-written directory is
  not "the newest copy".
- **Two new file names**: `ranks/<rank>/step` (each rank's own step; no `ranks/` means the
  single-writer path, unchanged) and `.ckpt-governor/layout.json` (a user's `glob`, `step_regex`,
  `declaration`, `probe_cmd`).
- **One new declaration location**: `<artifact>.resume.json`, beside Lightning's `.ckpt` or a Composer
  episode, with `target` naming the artifact. `<dir>/resume.json` is unchanged.
- **Two commands**: `wrap` and `declare`. `wrap` writes `stopping` and never `may-exit`; the controller
  still owns that file.

# 0023 — Conditions an operator adds, without adding code

Date: 2026-11-02. Status: accepted.

The catalogue stays frozen. What changes is that a deployment may add rows to it from disk, and the
boundary of the addition is the important part: a file probe can add a **name**, never a measurement,
a threshold or a new way to decide.

## Context

`docs/decisions/0013-risk-conditions.md` froze the catalogue behind three facts: a name, a producer,
and a test in the same commit. That works because the conditions this repository owns are the ones it
can measure from the directory: storage, the job's own cadence, the platform's announcements.

A deployment has conditions of its own that are not like those. "The loss exploded" is a fact the
training harness knows and the governor cannot compute; "the team's shared scratch volume is gone" is
a fact an operator knows. Both are already booleans on a disk the job can write, and refusing to watch
them costs a checkpoint — the same cost the risk core exists to avoid. The alternative, "write us a
probe", means a fork for one file read.

## Decision

1. **One file per condition, in `.ckpt-governor/conditions.d/*.json`.** It sits beside
   `conditions.json` and `deployment.json` because it is operator configuration and travels with the
   job.

   ```json
   {
     "name": "job.loss-exploded",
     "tier": "urgent",
     "missing": "urgent",
     "why": "the loss left its normal range",
     "read": {"file": "team-signal", "on": "1", "off": "0"}
   }
   ```

2. **A file probe, and nothing else.** `read.file` is a path inside the checkpoint directory; `on`
   and `off` are the two values that mean 1 and 0. A missing file or a third value is `unknown`, never
   0 — the operator's `missing` declaration decides what blindness means, exactly as for a built-in.

3. **Built-ins come first.** Added rows are appended after the frozen catalogue, so when an urgent
   built-in and an urgent added condition both fire, the decision names the built-in one. Precedence
   is consequence; a condition this repository owns is more certain than one an operator typed.

4. **A bad file fails startup.** A duplicate name (against the catalogue or another file), an unknown
   key, an unreadable tier, an absolute path or `on == off` is a refusal, not a default. The one thing
   this extension point must not allow is a run that silently watches fewer conditions than the operator
   believes.

5. **One directory's conditions are that directory's.** In `supervise`, `ProbesFor` and `PolicyFor` are
   built per unit from `conditions.d` in the unit's own directory; a supervisor with two jobs added
   different conditions does not share them.

## Evidence

### FACT

- `internal/conditions` — loader, validator and the file probe; `Set` carries the policy rows and the
  probes together because they come from the same file.
- `policy.Risk{Extra: []Condition}` — the zero value still watches the frozen set, which is why every
  existing caller and `TestTheCatalogueMatchesTheRecord` are unchanged.
- `ckptctl run --once` prints `conditions: 14 built-in + 1 from disk` when a directory adds one, and
  `TestAConditionFromDiskReachesTheDecision` drives a save named `job.loss-exploded` and a return to
  calm after the job writes `0`.

### REPORT

None. The request is a product requirement: a user asked for "one more condition" and a fork is the
wrong answer to it.

### ASSUMPTION

Most added conditions will be a boolean file the harness already writes per step. Falsified if the
first three adopters ask for a numeric threshold rather than a two-valued file; that would be a new
kind of extension (a comparison the operator chooses), and it needs its own record, not a flag on this
one.

## Consequences

- The frozen catalogue stays frozen: a new measurement still needs code, a card and a test.
- `docs/conditions.md` gains one section about the extension point, not a card per added name: cards
  describe what this repository produces, and added names are produced by the deployment.
- The probe costs one small read every five seconds, which is the storage conditions' cadence.

## What this does not promise

No numeric conditions, no conditions computed from world files, no way to override a built-in row, and
no authority: an added urgent condition can ask for a write, and it cannot do anything else.

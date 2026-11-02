# The disk and memory account: why it fills, and how it stops

A checkpoint product that fills the customer's volume is worse than doing nothing. This page answers
one question: **what grows, and what holds each of those down.**

## 1. Three things grow

| thing | unit | grows on | bound today |
|---|---|---|---|
| the generation directory `gen-N` | one checkpoint S | every real write | the retention rule (keep, plus the oldest step) |
| `.trash` | one S per retirement | every **retirement** | **no hard bound**, only a time budget |
| small files (requests, plan, ledger) | KB | every event | none (the ledger appends per event) |

**Memory is not in this table**, because it is O(1): the full hash streams into `sha256` through
`io.Copy`, and the tail check reads a fixed 1 MiB. This has to stay in the design — **the only way
back is somebody writing "read both files into memory and compare them" one day.**

## 2. What the steady state is

```
steady ≈ (keep + 1) × S   +   the trash high-water mark
          └ the kept copies ┘   └ retired, not yet swept ┘
```

The `+1` in `keep+1` is the copy being written: **it must exist at the same time, or a failed write
has nothing to fall back on.**

One example: S = 8 GiB, keep = 2 → 24 GiB steady, plus the trash peak. A 100 GiB volume holds that,
but only if trash has a bound.

## 3. Three things that exist and are not held down

1. **`.trash` has a time budget only** (`sweepTrash(dir, budget time.Duration)`). Time is the wrong
   measure for deletion: a slow disk does not finish the same bytes, a fast disk deletes too much;
   and `os.RemoveAll` on a directory **cannot be interrupted half-way**, so one 8 GiB directory can
   eat the whole budget and keep going.
   → count bytes instead: reclaim at most X MiB per round, stop at the budget, continue next round.

2. **Sweeping happens only on a write** (retirement happens in a write, the sweep in a round). A job
   that **stops writing** never sweeps its trash. That is exactly the worst case: preempted →
   retired → job ends → the space is held forever.
   → sweep on **every round**, not only when there was a write.

3. **The ledger appends per event, with no truncation.** One long job can push it to hundreds of
   thousands of lines.
   → keep the newest N entries plus one aggregate line ("M more entries before this run"), not all of
   them.

## 4. What happens when it is nearly full

The order is fixed and none of the steps can be skipped:

1. **count before writing:** is free space ≥ 1.2 × S? If not, retire the oldest copy, sweep one round
   of trash, and count again;
2. **if it still does not fit, refuse the write** and say **`cannot-fit`** — no retry, no partial
   write, and never by pushing out the last copy;
3. **report the numbers:** S is needed, X is available, retiring would free Y. A person can add a
   volume or change keep from those three.

**"Free space by pushing out somebody's last copy" is forbidden**: the failure mode of a checkpoint
product cannot be "I wrote, but deleted the copy you already had".

## 5. Three numbers to record on a real machine

Record them for the matrix and for a cluster run; with one missing, the case is untested:

- **bytes per preemption:** how many copies were written, and how large → is the steady state held by
  keep;
- **the trash high-water mark:** the peak between retirement and sweep, which must be below one S;
- **peak directory usage:** read after the 24-hour mark, not five minutes after the run.

## 6. What landed

**Implemented**

- sweep **by byte**, 256 MiB per round, one file at a time; a half-deleted directory continues next
  round;
- the sweep runs **every round**, not only when there was a write;
- `keep` drops to 1 under disk pressure (`keepUnderPressure`);
- a matrix `space` row: a 24 MiB tmpfs written until full → **three good copies still verify, the
  fourth gets ENOSPC, and the writer exits 1**.

```
space  generation 3  sealed 07:10:57  verified 07:10:58  sample  steps 3  (verified 3, writer exit 1)
```

- the long-run trash peak: forty rounds of continuous writing, peak below three copies, zero after
  writing stops. **This test failed the first time it ran** — a fixed budget lost ground every round
  and trash grew to 31 copies; the fix is "one round's budget must be ≥ what that round retires";
- ledger truncation: `FirstSeen` (one entry per written directory) keeps the newest 256. It is the
  only unbounded set in the ledger, and the ledger is **rewritten every round**, so the unbounded
  part is paid for every round.

**Still missing**

- the three numbers from an hours-long run on a real machine: bytes per preemption, the trash peak,
  and directory usage after 24 hours. The matrix and the unit tests run in seconds, so they cannot
  catch what it looks like after three days.

Until those exist, the honest sentence is: **the mechanism and the invariants exist; the bound over
a long run is inferred, not observed.**

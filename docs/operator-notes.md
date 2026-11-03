# Notes for the operator: what the platform must allow

This page is for the person who sets the stop deadline, not for the job author. The two numbers a user
configures are **periods** — minutes, or steps — and they are described in `README.md`. The numbers
below are the machine's constants; they exist so that a platform does not grant a deadline that a
checkpoint cannot meet.

## The chain, and the one deadline that matters

```
detect    one probe interval + one round      risk.shutdown: 5s + 5s  -> upper bound 10s
write     the job's own save                  measured 5.5 GiB in 16-22s on this volume
seal      read the shards back and hash them  measured 5.7-8.5s for the same 5.5 GiB
deadline  the platform's grace                must be >= the three above
```

A quieter run measured 11-13s and 4-4.6s for the same state; the range is the disk, not the policy.
Use your own measurement (below) rather than either end of it.

The sizing rule is a formula, not a promise:

```
grace >= (one probe interval + one round) + T_write + T_seal
```

**Measured once, on one box** (RTX 5070 Ti laptop, WSL vhdx, 0.5B model with AdamW, 5.5 GiB state):

| grace | outcome |
|---|---|
| 60s | **5 trials, 5 landed**; detected 1.7-2.9s, sealed 18.5-23.5s after the signal, **36.5-41.5s of margin**; `may-exit` 5/5; **0 lost steps**; every copy resumable |
| 60s, no handler | the counterfactual: a cadence-only arm (100-step interval here) landed **0/5**; lost 0-5 steps in this short trial, and nothing to resume from |
| 10s | impossible for any policy: the write alone is 16-22s on this volume |

The counterfactual is the honest comparison: a cadence loses up to one interval, and the governor's
arm is the one that closes the chain. The 20s wait after the seal is part of the trial, because
`may-exit` is the controller's answer *after* it saw the final copy, not the job's own write.

A grace below the chain does not produce a policy failure. It produces one of two honest outcomes:
the job is killed mid-write, or it leaves a broken copy. The deadline was smaller than the checkpoint.

## Getting your own `T_write`

Do not use a single `dd`: on this box the same 256 MiB file measured 219 MB/s and 1.2 GB/s five
minutes apart, because the first number is write-back and the second is page cache. Measure the write
the way the job does it — save the state, `fsync` the file and the directory, then read it back — and
keep the median of three. `T_seal` is the read-back: the governor hashes every shard at seal time
(decision 0025), so it costs about the same as reading the file once.

## Two numbers that must not be violated

- **`with-us` ≥ one write + one round.** Below that the window is unsatisfiable: the seal rearms it
  immediately. The plan answers `infeasible` instead of writing in a loop.
- **`grace` ≥ the chain above**, for any platform that kills on a timer.

## What this page does not promise

No probability, no percentile, no number of seconds for your cluster. The measured numbers are one
machine; the formula is what travels. A network volume is slower than this local vhdx, so the same
checkpoint needs a longer grace or a smaller checkpoint.

## Where the seconds belong

`docs/conditions.md` lists each probe's cadence, because that is the catalogue's own property.
Everything here is about a deadline the environment imposes; nothing here is a user-facing setting.

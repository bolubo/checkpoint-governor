# The real-machine work

What the product claims happens on an ordinary box, measured rather than modelled. Every stage writes
its numbers to `results/` (ignored by git, because a command reproduces them), and the numbers that
matter are quoted in `docs/`.

The question is not "how much does the default waste". That number is the arithmetic that *permits*
writing less (decision 0017: `no-us` is the baseline of the saving). The question is the one the
catalogue exists for: **does the chain close before the deadline** — signal, detection, request, a
sealed checkpoint, a verdict that it can resume.

```
t_signal -> t_detected -> t_request (requestId, reason = the condition's name) -> t_sealed -> t_verified
deadline D (grace / with-us / free < 2 x S)          margin = D - t_sealed
```

## Stages

| stage | what it measures | how | cost |
|---|---|---|---|
| S0 `s0_machine.sh` | the box: GPU, driver, torch, disk rate, fsync latency, RAM | `dd`, `nvidia-smi`, `/proc` | 5 s |
| S1 `s1_price.py` | `t_step`, `S` (model + optimizer), `T_write` (fsync'd file and directory) | a real 0.5B model on real text, 30 timed steps | 3 min |
| S2 `s2_shutdown.sh` | the announced-stop chain at four graces and two round cadences, plus the cadence-only counterfactual | real `SIGTERM`, real writes, N trials per cell | 30 min |

## What this network needs

`huggingface.co` does not resolve from this host, and `hf-mirror.com` serves files with metadata the
hub client refuses. `fetch.py` downloads the weights from ModelScope in range parts (~3 min for 0.5B)
and the wikitext parquet from the mirror. Everything lands under `experiments/real/`, which is ignored.

```bash
python3 experiments/real/fetch.py
```

## Running it

```bash
sh experiments/real/s0_machine.sh
python3 experiments/real/s1_price.py     # needs torch in this interpreter
TRIALS=3 sh experiments/real/s2_shutdown.sh
```

S2's cadence arm uses an interval scaled to the trial's length (a 20-second job never reaches 500
steps). Each row records that interval; the claim tested is "a cadence loses up to one interval", and
`docs/huggingface.md` states the real default.

## S2, 2026-11-03 (grace 60, 5s cadence, 5 trials)

`WAIT_AFTER_SEAL=20` is passed to `s2_job.py`: `may-exit` is the controller's answer after it saw the
final copy, and 2s was shorter than the handshake.

```
cell                  landed/N   margin min..max   lost max   reason
us        grace=60s every=5s  5/5          +36.5..+41.5  0          risk
default500 grace=60s every=5s  0/5                     -  5          -
```

`us` rows: detected 1.7-2.9s, sealed 18.5-23.5s after the signal, write 16.4-21.8s, seal 5.7-8.5s,
`may-exit` 5/5, resumable 5/5. The counterfactual's cadence is 100 steps here (the trial is shorter
than 500 steps); it loses up to one interval, which is the claim being tested.

### The boundary (`ARMS=us GRACES="30 10"`, 5 trials each)

```
us grace=30s every=5s  5/5  margin +7.1..+15.1   lost 0  may-exit 5/5
us grace=10s every=5s  0/5  margin  -9.9..-5.6   lost 0  may-exit 0/5
```

At grace 10 the write still sealed, 5.6-9.9s after the deadline: the harness lets the job finish so
the lateness is visible, while a real platform kill at 10s would leave a broken or absent copy. Grace
30 is the measured edge on this disk *for a good run*; the formula's worst case is 31.5-40.5s, so the
operator note still says use the formula, not the observed margin.

### The no-time probe (`ARMS=us GRACES=10 ESTIMATE=20s`)

With a declared write cost, the same grace 10 becomes a refusal rather than a late write:

```
us grace=10s every=5s  0/1  writes=0  exit=may-exit  lost=365  resumable=2
engine: skip no-time
```

This is the product behaving correctly with the information it was given: the job/operator declared
`T_write`; the platform's grace is still the platform's. Without the declaration the engine cannot
know and writes (fail-open), which is the late row above.

## S3: memory pressure and the end of a job

`host.memory-pressure` reads the process's cgroup. The host root is `max` (absent), so the test is a
container with `--memory=2g` and a `memhog` in the same cgroup:

```sh
TRIALS=3 sh experiments/real/s3_memory.sh
```

N=3: the condition is **1**, the first write lands at step 496-498, the next attempt a few steps later
is refused by the 60s cooldown, and at 40s the step is 540-541 while the newest copy is at 496-498
(lost 42-45); `oom_kill` 0 because the hog holds 1.94/2 GiB. The condition is **persistent**, not an
event; the copy is the protection, and the cooldown is the retry.

A real OOM death, after the write the probe asked for (the hog creeps past the limit instead of
crossing it in one allocation):

```sh
OOM=1 HOG=2000 CREEP=2 CREEP_MS=500 TRIALS=3 sh experiments/real/s3_memory.sh
```

N=3: `oom_kill` **1/1/1**, the first write at step 494/494/495, the job and the controller **both
alive** afterwards (the kernel killed the hog, not the job), and the distance from the write to the
OOM 148-203 steps (3.0-4.1 s). A persistent pressure keeps the 60 s retry: it is a cost bound, and
re-deciding every round would turn one alarm into a write storm.

The normal end of a run:

```sh
TRIALS=3 sh experiments/real/s3_ending.sh
```

N=3: `may-exit` 3/3, the final copy at step 49, lost 0. The state file after the handshake reads
`skip/calm`; the proof is the copy and `may-exit`, not the last plan's sentence.

## What is measured, and what is not

- `S` and `T_write` are for **this** disk. `T_write` is a lower bound for a cluster volume: it is a
  local vhdx, not NFS or Lustre.
- The model is real (Qwen2.5-0.5B, wikitext-2), the optimizer is real (AdamW), the state is real
  (model + optimizer, `torch.save` + `fsync`). The *job* is a script, not a framework: there is no
  `Trainer` here, because the chain is about the governor and the writer, not about Hugging Face.
- The stop is a real `SIGTERM` from outside the process. It is not a scheduler: no Kueue, no pod
  deletion. `risk.preempt` through a kind cluster is a later stage, and `make spot` already covers the
  cluster path.
- A laptop GPU throttles. `t_step` is measured inside the same run that reports it, never reused.

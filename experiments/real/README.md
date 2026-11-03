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
/root/llamafactory-venv/bin/python experiments/real/s1_price.py
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

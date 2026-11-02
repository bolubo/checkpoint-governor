# What the defaults cost

Status: model, 2026-11-02. Companion: `docs/plan-library.md` (P0.4), decision 0028. Regenerate with
`make waste`; every number below is printed by that command.

The frameworks do not know what you are training. Their default interval is a guess, and the guess is
expensive: it writes checkpoints whether or not anything is at risk. This page prices that guess — as a
model, with its assumptions printed first.

## The formula

```
waste fraction = T_write / (interval × t_step)
```

`T_write = params × bytes-per-param / throughput`. The fraction is **independent of how long the job
runs**: a four-hour job and a forty-hour job waste the same share. That is why three numbers are enough
to check any row.

The defaults, read from their source:

| framework | default interval | default retention | source |
|---|---|---|---|
| Hugging Face | 500 steps | **all** (`save_total_limit=None`) | `training_args.py` |
| Lightning | one epoch | 1 (`save_top_k=1`) | `model_checkpoint.py` |
| Composer | one epoch (`1ep`) | **all** (`num_checkpoints_to_keep=-1`) | `checkpoint_saver.py` |
| Megatron | **none** — `--save` requires `--save-interval` | — | `arguments.py` |

## The table

Assumptions: a four-hour job, 16 bytes per parameter (fp16 weight + fp32 master + Adam moments),
2 GB/s aggregate write throughput, 1000 steps per epoch, and illustrative step times (0.5s, 1s, 2s, 4s
for the four scales). None of these is a benchmark; replace them with your own.

| scale | checkpoint | write | Hugging Face: waste / GPU-hours / kept | Lightning: waste / kept | Composer: waste / kept |
|---|---|---|---|---|---|
| 100M / 1 GPU | 1.6 GB | 1s | 0.3% · 0.0 · 91 GB | 0.2% · 2 GB | 0.2% · 45 GB |
| 1B / 8 GPU | 16 GB | 8s | **1.6% · 0.5 · 448 GB** | 0.8% · 16 GB | 0.8% · 224 GB |
| 7B / 64 GPU | 112 GB | 56s | **5.6% · 14.3 · 1.6 TB** | 2.8% · 112 GB | 2.8% · 784 GB |
| 70B / 512 GPU | 1.1 TB | 560s | **28% · 573 · 7.8 TB** | 14% · 1.1 TB | 14% · 3.4 TB |

Read the 70B row as: one job, four hours, 512 GPUs — and 28% of the GPU time is spent writing
checkpoints that a job which finishes never reads. That is 573 GPU-hours per job, and 7.8 TB left on
disk when the retention default keeps every copy.

## What this does not claim

- **Synchronous writes.** The table charges the write time to the job. Async saves (DCP `async_save`,
  Orbax) hide some of it behind compute; they do not reduce bytes, bandwidth or storage. The 28% is the
  synchronous default, not every deployment.
- **Only writes that are never read.** Best-model and evaluation checkpoints are excluded. If a team
  reads its checkpoints, the interval is not waste; it is a workflow.
- **Step times are illustrative.** They say "this shape, these scales", not "your cluster".
- **A write longer than the interval is infeasible**, and the command says so instead of printing a
  fraction above 100%.
- **No claim about us being cheaper at the same interval.** If a user configures the same interval we
  would, we write the same number. The saving is in **not configuring at all** — the default is zero
  periodic writes — or in a bound that is longer than the framework's guess.

## Using your own numbers

```bash
make waste FRAMEWORK=huggingface HOURS=8 BYTES_PER_PARAM=18 THROUGHPUT=0.4 STEPS_PER_EPOCH=1000
```

The command always prints the assumptions and the source of the default it used. A table without them
would be a rumour.

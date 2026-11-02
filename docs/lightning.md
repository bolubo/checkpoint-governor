# Lightning

Status: supported integration, 2026-11-02. The contract is tested in CI without lightning installed.

## The diff

Remove `ModelCheckpoint` (it would keep writing on its own schedule) and add one callback:

```python
from ckpt_policy import Governor
from ckpt_policy.lightning import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
trainer = Trainer(max_epochs=..., default_root_dir="/ckpt/job-1",
                  callbacks=[GovernorCallback(g)])
trainer.fit(model, dataloader)
trainer.save_checkpoint("/ckpt/job-1/final.ckpt")   # the job's own end write
```

## What happens

| event | who writes | when |
|---|---|---|
| nothing at risk | nobody | during training: zero writes |
| the plan asks (a condition, or your bound) | `trainer.save_checkpoint` | at the next batch end |
| the platform announces a stop | the same path, when the plan fires `risk.preempt` | at the next batch end |
| end of training | your `save_checkpoint` | once |

The latency is **one batch**, because Lightning's `save_checkpoint` is public. That is the difference
from the Trainer integration, whose latency is `logging_steps`.

## Two things to know

1. **The file is Lightning's, not ours.** The callback writes a `.ckpt` through Lightning's own API; our
   `gen-N` layout is not involved. This integration decides *when*; `ckptctl resumable` still applies to
   the layouts our writer produces.
2. **Do not run both.** `ModelCheckpoint` plus this callback is two schedules for one job. Remove it.

## What the receipt should say

After the first mid-run write, `ckptctl status --dir` shows a write whose kind is `window` or
`exception`, not `final`; the callback records `ending` at the end of training.

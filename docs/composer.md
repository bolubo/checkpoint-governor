# Composer

Status: supported integration, 2026-11-02. The contract is tested in CI without composer installed.

## The diff

Remove `CheckpointSaver` and add the callback:

```python
from ckpt_policy import Governor
from ckpt_policy.composer import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
trainer = Trainer(
    model=model, train_dataloader=loader, max_duration="4h",
    save_folder="/ckpt/job-1",
    callbacks=[GovernorCallback(g)],       # and no CheckpointSaver
)
trainer.fit()
```

## What happens

| event | who writes | when |
|---|---|---|
| nothing at risk | nobody | during training: zero writes |
| the plan asks (a condition, or your bound) | Composer's own `save_checkpoint(state, filename=…)` | at the next batch end |
| the platform announces a stop | the same path, when the plan fires `risk.preempt` | at the next batch end |
| end of training | your own save | once |

The callback imports `composer.utils.checkpoint` inside `batch_end`, so this module is importable on a
machine without Composer — the package keeps zero runtime dependencies.

## Two things to know

1. **The file is Composer's layout, not our `gen-N`.** This integration decides *when*; `ckptctl
   resumable` still applies to the layouts our writer produces.
2. **`CheckpointSaver` was also the retention and the `latest-rank{rank}` pointer.** Removing it means
   those are yours: the policy decides when, not what to keep. That is a real trade, and it is why this
   page says it out loud instead of implying a drop-in replacement.

## What the receipt should say

After the first mid-run write, `ckptctl status --dir` shows a write whose kind is `window` or
`exception`, not `final`; the callback records `ending` at the end of training.

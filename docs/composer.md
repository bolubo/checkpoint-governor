# Composer

Status: supported integration, 2026-11-02. The contract is tested in CI without composer installed.

## The diff

Add the callback and pass back the two arguments `CheckpointSaver` used to own:

```python
from ckpt_policy import Governor
from ckpt_policy.composer import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
trainer = Trainer(
    model=model, train_dataloader=loader, max_duration="4h",
    save_folder="/ckpt/job-1",
    callbacks=[GovernorCallback(g, num_checkpoints_to_keep=2, latest_filename="latest-rank{rank}")],
)
trainer.fit()
```

`composer.utils.checkpoint.save_checkpoint` is public, and both arguments are passed to it, so the
retention and the `latest-rank{rank}` pointer are not lost.

## What stays the framework's

| guarantee | kept by |
|---|---|
| file layout, atomic write | Composer's `save_checkpoint` |
| retention | `num_checkpoints_to_keep` passed through |
| newest pointer | `latest_filename` passed through |

## What it declares

After every write, rank 0 writes `<artifact>.resume.json` beside the episode — `target` names the
artifact, `requires` is the four framework states, and the caveat `data-position-not-saved` is
recorded. Other ranks do not write a declaration: one file per shard would let a reader pick a rank's
view as if it were the whole. If the installed Composer cannot name the file it wrote, the callback
records that in `cb.notes` instead of writing a declaration about something else.

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

1. **The file is Composer's layout, not our `gen-N`.** The declaration beside the episode is what makes
   `ckptctl resumable` able to answer for it; our own writer is not involved.
2. **`CheckpointSaver`'s two guarantees are passed back explicitly.** If the installed Composer version's
   `save_checkpoint` does not accept `num_checkpoints_to_keep`/`latest_filename`, the callback still
   makes the write and records why in `cb.notes` — it never pretends the pointer was written.

## What the receipt should say

After the first mid-run write, `ckptctl status --dir` shows a write whose kind is `window` or
`exception`, not `final`; the callback records `ending` at the end of training.

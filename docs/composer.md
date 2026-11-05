# Composer

Status: supported integration, 2026-11-02. The contract is tested in CI without composer installed.

## Paste this

```bash
python3 -m venv .venv      # a system Python under PEP 668 refuses pip without one
. .venv/bin/activate
pip install ckpt-policy
```

```python
from ckpt_policy import Governor
from ckpt_policy.composer import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
# hand it to your Trainer: callbacks=[GovernorCallback(g, num_checkpoints_to_keep=2)]
```

The block installs and wires the callback; the rest of this page explains what it does.

## The diff

Keep the saver, turn its own schedule off, and let the callback drive it:

```python
from ckpt_policy import Governor
from ckpt_policy.composer import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
trainer = Trainer(
    model=model, train_dataloader=loader, max_duration="4h",
    save_folder="/ckpt/job-1",
    save_interval=lambda state, event: False,   # no schedule of its own
    save_num_checkpoints_to_keep=2,             # retention stays the framework's
    save_latest_filename="latest-rank{rank}.pt",
    callbacks=[GovernorCallback(g)],
)
trainer.fit()
```

`Trainer(save_folder=...)` creates Composer's own `CheckpointSaver`; the callback finds it and calls
its own `_save_checkpoint` and `_rotate_checkpoints`. That is where the folder, the retention and the
`latest-rank{rank}` symlink live, so none of them is lost by installing the callback.

## What stays the framework's

| guarantee | kept by |
|---|---|
| file layout, atomic write | Composer's `CheckpointSaver` |
| retention | `save_num_checkpoints_to_keep` on the saver the callback drives |
| newest pointer | `save_latest_filename`, written by the saver |

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
| the plan asks (a condition, or your bound) | the `CheckpointSaver` you kept | at the next batch end |
| the platform announces a stop | the same path, when the plan fires `risk.preempt` | at the next batch end |
| end of training | your own save | once |

The callback imports `composer.utils.checkpoint` inside `batch_end`, so this module is importable on a
machine without Composer — the package keeps zero runtime dependencies.

## Two things to know

1. **The file is Composer's layout, not our `gen-N`.** The declaration beside the episode is what makes
   `ckptctl resumable` able to answer for it; our own writer is not involved.
2. **The public `save_checkpoint` is not enough.** In Composer 0.32.1 it writes a file but takes neither
   `num_checkpoints_to_keep` nor `latest_filename`; only the `CheckpointSaver` updates those. The
   callback therefore drives the saver's own `_save_checkpoint`/`_rotate_checkpoints` when one exists,
   and when it does not it still writes, records the lost guarantees in `cb.notes`, and never pretends
   the pointer was written.

## What the receipt should say

After the first mid-run write, `ckptctl status --dir` shows a write whose kind is `window` or
`exception`, not `final`; the callback records `ending` at the end of training.

# Lightning

Status: supported integration, 2026-11-02. The contract is tested in CI without lightning installed.

## Paste this

```bash
python3 -m venv .venv      # a system Python under PEP 668 refuses pip without one
. .venv/bin/activate
pip install ckpt-policy
```

```python
from ckpt_policy import Governor
from ckpt_policy.lightning import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
# hand it to your Trainer: callbacks=[GovernorCallback(g, saver=ckpt)]
```

The block installs and wires the callback; the rest of this page explains what it does.

## The diff

Keep `ModelCheckpoint`, but turn its own schedule off, and hand it to the callback:

```python
from ckpt_policy import Governor
from ckpt_policy.lightning import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
ckpt = ModelCheckpoint(
    dirpath="/ckpt/job-1",
    save_top_k=2,
    every_n_train_steps=None,          # no schedule of its own
    every_n_epochs=None,
    save_on_train_epoch_end=False,
)
trainer = Trainer(max_epochs=..., default_root_dir="/ckpt/job-1",
                  callbacks=[GovernorCallback(g, saver=ckpt)])
trainer.fit(model, dataloader)
trainer.save_checkpoint("/ckpt/job-1/final.ckpt")   # the job's own end write
```

The saver is not removed. When the plan asks, the callback calls the saver's own
`_save_topk_checkpoint` and `_save_last_checkpoint`, so `save_top_k`, `last_model_path` and
`best_model_path` keep doing exactly what they did before we were installed. The public
`save_checkpoint` writes a file but skips that bookkeeping — a real run left `best_model_path` empty —
so the private routines are used when present, and the callback falls back to the public call (recording
the lost guarantees in `cb.notes`) when they are not.

## What stays the framework's

| guarantee | kept by |
|---|---|
| file format, atomic write, metadata | Lightning's `save_checkpoint` |
| top-k retention | the `ModelCheckpoint` you kept |
| newest pointer | `last_model_path` |
| best pointer | `best_model_path` |

Without a `saver`, the callback falls back to `trainer.save_checkpoint(path)`: Lightning's format, but
retention and the two pointers are then yours to manage. That fallback is named in the code, not
implied.

## What it declares

After every write, the callback writes `<file>.ckpt.resume.json` beside the file the saver produced —
`target` names that file, and `requires` is the four states a framework checkpoint can carry. The
caveat `data-position-not-saved` is recorded for the same reason as in the Trainer integration. So
`ckptctl resumable --dir` answers for the `.ckpt` itself:

```
resumable (step 40, requires model, optimizer, scheduler, rng, caveat: the data position is not saved; …)
```

## What happens

| event | who writes | when |
|---|---|---|
| nothing at risk | nobody | during training: zero writes |
| the plan asks (a condition, or your bound) | the `ModelCheckpoint` you kept | at the next batch end |
| the platform announces a stop | the same path, when the plan fires `risk.preempt` | at the next batch end |
| end of training | your `save_checkpoint` | once |

The latency is **one batch**, because the hook is the batch end and nothing in the framework schedules
the save. That is the difference from the Trainer integration, whose latency is `logging_steps`.

## Two things to know

1. **The file is Lightning's, not ours.** The callback writes a `.ckpt` through Lightning's own API; our
   A `.ckpt` is a single artifact, not a directory, so it gets `<file>.resume.json` and no manifest:
   `resumable` answers from the declaration alone, and that is the one layout where nothing checks
   the bytes. The directory layouts (Hugging Face, Composer) are indexed.
   `gen-N` layout is not involved. The declaration beside it is what makes `ckptctl resumable` able to
   answer for that file.
2. **The saver's own schedule must be off.** `ModelCheckpoint` plus this callback is two schedules for
   one job unless `every_n_train_steps` and `every_n_epochs` are `None` and
   `save_on_train_epoch_end=False`, as in the recipe above. Turning the schedule off is the point;
   removing the saver is not.

## What the receipt should say

After the first mid-run write, `ckptctl status --dir` shows a write whose kind is `window` or
`exception`, not `final`; the callback records `ending` at the end of training.

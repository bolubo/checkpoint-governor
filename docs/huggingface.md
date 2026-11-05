# Hugging Face Trainer

Status: supported integration, 2026-11-02. The contract is tested in CI without torch; a real run needs
`pip install torch transformers`.

## Paste this

```bash
python3 -m venv .venv      # a system Python under PEP 668 refuses pip without one
. .venv/bin/activate
pip install ckpt-policy
```

```python
from ckpt_policy import Governor
from ckpt_policy.hf import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
# hand it to your Trainer: callbacks=[GovernorCallback(g)]
```

The block installs and wires the callback; the rest of this page explains what it does.

## The diff

```python
from ckpt_policy import Governor
from ckpt_policy.hf import GovernorCallback

g = Governor("/ckpt/job-1", start_engine=True)
args = TrainingArguments(
    output_dir="/ckpt/job-1",
    save_strategy="steps",          # keep the framework's saver; only its alarm is ours to move
    save_steps=10_000,              # a step the run never reaches: the cadence is ours now
    save_total_limit=2,             # retention stays the framework's
    eval_strategy="steps",
    eval_steps=10,                  # must equal logging_steps for best tracking (see below)
    load_best_model_at_end=True,    # best stays the framework's
    enable_jit_checkpoint=True,     # HF's own SIGTERM save — off by default
    logging_steps=10,               # a request is honoured at the next logging step
)
trainer = Trainer(model, args, train_dataset=..., eval_dataset=..., callbacks=[GovernorCallback(g)])
trainer.train()
trainer.save_model("/ckpt/job-1/final")   # optional weights-only copy for inference
g.close()                                 # stop the engine this client started
```

`eval_dataset` is required by `load_best_model_at_end`; without it the Trainer refuses the run before
training starts.

**We do not remove the saver.** `save_strategy="steps"` with a `save_steps` the run never reaches turns
the alarm off; everything the saver does — retention, the latest pointer, best tracking, atomic
write — keeps working exactly as before we were installed.

## What stays the framework's

| guarantee | kept by |
|---|---|
| atomic write, file layout, metadata | HF's own checkpoint path |
| retention (`save_total_limit`) | HF's rotation, on every save we request |
| latest checkpoint | HF's `checkpoint-<step>` naming |
| best checkpoint | HF's `load_best_model_at_end` + `best_model_checkpoint` |

Ours is only the timing: when a write is asked for, and what the receipt says about it.

## What it declares

After every save, `on_save` writes `checkpoint-<step>/resume.json` with `target: "."`, the states the
Trainer actually wrote, and `requires: model, optimizer, scheduler, rng`. It also records the caveat
`data-position-not-saved`: a Trainer checkpoint has never held a data-loader position, so a restore
re-iterates and skips. That is why `ckptctl resumable --dir` can answer for a Hugging Face checkpoint
without being told anything else:

```
resumable (step 1000, requires model, optimizer, scheduler, rng, caveat: the data position is not saved; …)
```

`save_only_model=True` is a fact, not a defect: the declaration then says `optimizer`, `scheduler` and
`rng` are absent, and the verdict is `not resumable: optimizer state absent`.

## What happens

| event | who writes | when |
|---|---|---|
| nothing at risk | nobody | during training: zero writes |
| the plan asks (a condition, or your bound) | HF's own checkpoint path | at the next logging step |
| the platform announces a stop | HF's JIT checkpoint (`enable_jit_checkpoint`) | on SIGTERM |
| end of training | HF's own saver (the last checkpoint); `save_model` is an optional weights-only copy | once |

The callback itself is four lines: `on_step_end` sets `control.should_save` when `should_save()` is
true, and `on_train_end` records `ending`. The decision stays in the engine; the callback only relays.
Transformers 5 calls **every** event on every callback, so the class subclasses `TrainerCallback` when
transformers is installed and stays a plain class when it is not.

## Observed

With torch 2.13.0+cu130 / transformers 5.6.0, `make hf` on a 1000-step CPU job:

```
framework default (save every 500 steps)   2 checkpoint(s)
ckpt-policy, 2s window                     2 checkpoint(s) while training
end write                                   True (final/)

guarantees kept by using the framework's own saver
  retention   2 checkpoint(s), save_total_limit=2
  latest      checkpoint-1000
  best        checkpoint-100
```

With no window (the unconfigured posture), the same 1000-step run wrote **0** checkpoints while training
and **1** at the end (`checkpoint-1000`), and `resumable` answered for it. That is the S1 run on
2026-11-05; the window arm above is what `make hf` uses so the comparison has a periodic write to show.
The unconfigured arm also left `best_model_checkpoint` empty — caveat 3 explains why.

## Three caveats, stated rather than hidden

1. **The latency is `logging_steps`.** The Trainer consumes `control.should_save` at its logging
   boundary. With the default `logging_steps=500`, a request can wait 500 steps; set it small (10 is
   fine) when the request has to be honoured quickly.
2. **There is no public full-checkpoint API.** `Trainer.save_model` saves weights, not optimizer state.
   A fully resumable final checkpoint needs the same routine you use during training; HF's
   `_save_checkpoint` is private, and this page does not pretend otherwise.
3. **`load_best_model_at_end` needs a save at an evaluation boundary.** HF records a best checkpoint
   only when a save and an evaluation land on the same step. With the alarm off and no window, the only
   save is the last one, so `best_model_checkpoint` can stay empty even when `eval_steps` equals
   `logging_steps` — the S1 run on 2026-11-05 saw exactly that. Use a window, or a `save_steps` multiple
   the run reaches, if best tracking matters. That is HF's own rule — not a saver we removed.

## What the receipt should say

After the first mid-run write, `ckptctl status <dir>` should show a write whose kind is not `final`
(a `window` or an `exception` write), and `ckptctl resumable --dir` should answer for the
`checkpoint-<step>` the Trainer wrote — not for a `gen-*` directory, because this integration does not
write one. The framework's own `latest` pointer is not ours; we never move it.

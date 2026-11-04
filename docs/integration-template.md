# Writing an integration

Status: template, 2026-11-02. Use this when adding a framework to `contrib/ckpt-policy`.

An integration answers one question per step and writes through the framework's own save path. It does
not re-implement the policy, and it does not own the checkpoint format.

## The contract

1. **Ask once per step (or per batch).** `governor.should_save()` is the whole question.
2. **Write through the framework's saver.** Keep the saver object the framework already has
   (`ModelCheckpoint`, `CheckpointSaver`, the Trainer's own path) and call it. Never remove a saver to
   take over its schedule: it owns retention, the latest pointer and best tracking, and those are not
   ours to lose.
3. **Mark the request served.** `governor.served(gen)` — otherwise the same generation is asked for on
   every step. (`sealed()` is the exception: it records the request while writing our metadata.)
4. **Record the end.** `governor.ending()` once, when training finishes.
5. **Never import the framework at module import.** Use a lazy import, or an optional base class:
   `try: from framework import Callback as _Base; except ImportError: class _Base: pass`.
6. **Turn the framework's own cadence off in the page's diff.** Two schedules is worse than one; a
   removed saver is worse than both.
7. **The glue budget is three lines.** If a page needs more, the gap is ours, not the user's.

## The skeleton

```python
class GovernorCallback:
    def __init__(self, governor):
        self.governor = governor

    def on_step(self, trainer):
        gen = self.governor.requested()
        if not gen:
            return
        trainer.save_checkpoint(path_for(trainer))   # the framework's own path
        self.governor.served(gen)

    def on_end(self, trainer):
        self.governor.ending()
```

## The test, without the framework

The runner has no torch, so the contract test drives a fake object that models exactly two behaviours:

- the framework's cadence (so the baseline count is real), and
- how a request is honored (immediately, or at a boundary such as `logging_steps`).

Assert three things: silence → no writes; one request → one write; the end → `ending` recorded. If the
framework's callback base calls every event (Transformers 5 does), the fake must accept the same.

## The page must state

- **Latency**: one step, one batch, or `logging_steps`.
- **What it does not provide**: the format is the framework's, not our `gen-N`; `ckptctl resumable`
  applies to our layout. If removing the framework's saver also removes retention or a `latest`
  pointer, say so.
- **The receipt**: which `ckptctl status` line should appear after the first request.

## Files to add

| file | why |
|---|---|
| `ckpt_policy/<framework>.py` | the callback, no framework import at module load |
| `tests/test_governor.py` | one class, three cases, a fake trainer |
| `docs/<framework>.md` | the diff, the events table, the caveats, the observed numbers |
| `README.md` | one line in the documentation list |
| `docs/plan-library.md` | the row, with its done-when and observed result |

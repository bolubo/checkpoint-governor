"""A Hugging Face Trainer callback, without importing transformers at module load.

    args = TrainingArguments(save_strategy="no", enable_jit_checkpoint=True, logging_steps=10)
    trainer = Trainer(..., args=args, callbacks=[GovernorCallback(g)])

The callback answers one question per step: `control.should_save = governor.should_save()`. The Trainer
then writes its own checkpoint at the next logging step, so the request is honored within `logging_steps`
— that is the latency bound, and it is why the recipe sets it small.

The announced stop is `enable_jit_checkpoint=True`, which is HF's own SIGTERM save and off by default.
The final write is the job's own save routine; this callback records `ending` so the engine knows the job
finished.

Transformers 5 calls *every* event on every callback, so this class subclasses `TrainerCallback` when
transformers is installed (it inherits the no-op hooks) and stays a plain class when it is not — the
module remains importable on a machine without transformers.
"""

try:
    from transformers import TrainerCallback as _TrainerCallback
except ImportError:  # pragma: no cover - the package keeps zero runtime dependencies
    class _TrainerCallback:  # noqa: D101 - the stand-in only needs to exist
        pass


class GovernorCallback(_TrainerCallback):
    """TrainerCallback-compatible: the two hooks used are `on_step_end` and `on_train_end`."""

    def __init__(self, governor):
        try:
            super().__init__()
        except TypeError:  # older transformers take no argument; none is needed
            pass
        self.governor = governor
        self.requests = 0

    def on_step_end(self, args, state, control, **kwargs):
        if self.governor.should_save():
            control.should_save = True
            self.requests += 1
        return control

    def on_train_end(self, args, state, control, **kwargs):
        self.governor.ending()
        return control

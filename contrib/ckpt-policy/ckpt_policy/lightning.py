"""A Lightning callback, without importing lightning.

    trainer = Trainer(..., callbacks=[GovernorCallback(g)])     # and no ModelCheckpoint

Lightning writes its own `.ckpt` through the public `trainer.save_checkpoint(path)`, so the request is
honoured at the next batch end — one batch of latency, not a logging interval. The callback records the
generation as served, because Lightning never calls our `sealed()`.

The format is Lightning's, not our `gen-N` layout: this integration decides *when*, and `ckptctl
resumable` still applies to the layouts our own writer produces.
"""

import os


class GovernorCallback:
    """pl.Callback-compatible by duck typing: Lightning inspects the methods it needs."""

    def __init__(self, governor, filename=None):
        self.governor = governor
        # A string, or a callable taking the trainer; None means "<default_root_dir>/ckpt-step-<n>.ckpt".
        self.filename = filename
        self.saves = 0

    def on_train_batch_end(self, trainer, pl_module):
        gen = self.governor.requested()
        if not gen:
            return
        trainer.save_checkpoint(self._path(trainer))
        self.governor.served(gen)
        self.saves += 1

    def on_train_end(self, trainer, pl_module):
        self.governor.ending()

    def _path(self, trainer):
        if callable(self.filename):
            return self.filename(trainer)
        if self.filename:
            return self.filename
        return os.path.join(str(trainer.default_root_dir), f"ckpt-step-{trainer.global_step}.ckpt")

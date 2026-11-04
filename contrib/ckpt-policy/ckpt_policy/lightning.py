"""A Lightning callback, without importing lightning.

    ckpt = ModelCheckpoint(dirpath=..., save_top_k=2, every_n_train_steps=None,
                           every_n_epochs=None, save_on_train_epoch_end=False)
    trainer = Trainer(..., callbacks=[GovernorCallback(g, saver=ckpt)])

The saver stays. Its cadence is off (`every_n_train_steps=None`, `every_n_epochs=None`,
`save_on_train_epoch_end=False`), so it never writes on its own; when the plan asks, we call its own
`save_checkpoint(trainer)`, which keeps `save_top_k`, `last_model_path` and `best_model_path` doing
what they did before we were installed.

Without a `saver`, the callback falls back to the public `trainer.save_checkpoint(path)`: Lightning's
own format, but retention and the two model-path pointers are then yours to manage.
"""

import os

from .governor import FRAMEWORK_REQUIRES

# A framework checkpoint has never held a data-loader position: the dataset is re-iterated and skipped.
DATA_CAVEAT = ["data-position-not-saved"]


class GovernorCallback:
    """pl.Callback-compatible by duck typing: Lightning inspects the methods it needs."""

    def __init__(self, governor, filename=None, saver=None):
        self.governor = governor
        # A string, or a callable taking the trainer; None means "<default_root_dir>/ckpt-step-<n>.ckpt".
        self.filename = filename
        self.saver = saver
        self.saves = 0

    def on_train_batch_end(self, trainer, pl_module):
        gen = self.governor.requested()
        if not gen:
            return
        if self.saver is not None and hasattr(self.saver, "save_checkpoint"):
            # The saver's own path: top-k, the latest pointer and the best pointer are its business.
            self.saver.save_checkpoint(trainer)
            written = self._newest_ckpt(getattr(self.saver, "dirpath", "") or "")
        else:
            written = self._path(trainer)
            trainer.save_checkpoint(written)
        # The framework owns the layout; the leader writes the one statement a restore is judged
        # against. A non-leader's shard is its own, and its declaration would speak for the episode.
        if written and os.path.exists(written) and self.governor.rank0:
            self.governor.declare(written, step=int(getattr(trainer, "global_step", 0) or 0),
                                  format="other", requires=list(FRAMEWORK_REQUIRES),
                                  caveats=DATA_CAVEAT)
        self.governor.served(gen)
        self.saves += 1

    def on_train_end(self, trainer, pl_module):
        if self.governor.rank0:
            self.governor.ending()

    def _path(self, trainer):
        if callable(self.filename):
            return self.filename(trainer)
        if self.filename:
            return self.filename
        return os.path.join(str(trainer.default_root_dir), f"ckpt-step-{trainer.global_step}.ckpt")

    @staticmethod
    def _newest_ckpt(dirpath):
        """The file the saver just wrote: the `.ckpt` with the newest mtime under `dirpath`.

        `ModelCheckpoint.save_checkpoint` returns nothing, and its own `last_model_path` is the
        framework's pointer, not the file this request wrote. The newest file is the only answer that
        does not invent a naming rule.
        """
        best, best_at = "", -1.0
        for root, _dirs, files in os.walk(str(dirpath)):
            for name in files:
                if not name.endswith(".ckpt"):
                    continue
                path = os.path.join(root, name)
                at = os.stat(path).st_mtime
                if at > best_at:
                    best, best_at = path, at
        return best

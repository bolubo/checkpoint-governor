"""A Composer callback, without importing composer.

    trainer = Trainer(
        ..., save_folder="/ckpt/job-1", save_interval="0ba",   # the saver stays; its schedule is off
        save_num_checkpoints_to_keep=2, save_latest_filename="latest-rank{rank}.pt",
        callbacks=[GovernorCallback(g)],
    )

The saver stays. `Trainer(save_folder=...)` creates Composer's own `CheckpointSaver`; the callback
finds it and, when the plan asks, calls the saver's own `_save_checkpoint` and `_rotate_checkpoints`.
That is where the folder, the `num_checkpoints_to_keep` rotation and the `latest-rank{rank}` symlink
live. The public `save_checkpoint` function accepts a filename but none of those arguments in 0.32.1 —
a real run showed it — so the saver is used when present and the fallback records what it could not do.

The saver's own schedule must be off (`save_interval="0ba"`); otherwise it is two schedules for one
job. Turning the schedule off is the point; removing the saver is not.
"""

import os

from .governor import FRAMEWORK_REQUIRES

# Composer dispatches every event through `Callback.run_event`, so a plain object fails at `init` —
# the same shape of bug a real Lightning run exposed. Subclass `Callback` when composer is installed
# (it inherits the no-op hooks) and stay plain otherwise, so this module remains importable without it.
try:
    from composer.core import Callback as _Callback
except ImportError:  # pragma: no cover - the package keeps zero runtime dependencies
    class _Callback:  # noqa: D101 - the stand-in only needs to exist
        pass

# A framework checkpoint has never held a data-loader position: the dataset is re-iterated and skipped.
DATA_CAVEAT = ["data-position-not-saved"]


class GovernorCallback(_Callback):
    """Callback-compatible: the hooks used are `init`, `batch_end` and `fit_end`."""

    def __init__(self, governor, filename="ep{epoch}-ba{batch}-rank{rank}"):
        self.governor = governor
        self.filename = filename
        self.saver = None
        self.notes = []
        self.saves = 0

    def init(self, state, logger):
        # The Trainer creates a CheckpointSaver from `save_folder`; it is the object that owns the
        # folder, the rotation and the latest pointer. Finding it once is cheaper than re-deriving
        # its path rules here.
        try:
            from composer.callbacks.checkpoint_saver import CheckpointSaver
        except ImportError:  # pragma: no cover - only reachable with an incompatible Composer
            return
        for callback in getattr(state, "callbacks", []):
            if isinstance(callback, CheckpointSaver):
                self.saver = callback
                return

    def batch_end(self, state, logger):
        gen = self.governor.requested()
        if not gen:
            return

        written = ""
        if self.saver is not None:
            # The saver's own routine: folder formatting, rotation and the latest symlink are its
            # business, and the public function does not take those arguments in this version.
            before = set(getattr(self.saver, "all_saved_checkpoints_to_timestamp", {}))
            self.saver._save_checkpoint(state, logger)
            self.saver._rotate_checkpoints(getattr(state, "fsdp_sharded_state_dict_enabled", False))
            after = set(getattr(self.saver, "all_saved_checkpoints_to_timestamp", {}))
            if after - before:
                written = sorted(after - before)[-1]
        else:
            # No saver: Composer's format, but retention and the latest pointer are the caller's.
            from composer.utils import checkpoint as composer_checkpoint
            written = composer_checkpoint.save_checkpoint(state, filename=self.filename) or ""
            self.notes.append(
                "composer: no CheckpointSaver was found; retention and the latest pointer are yours")

        # Only rank 0 says what the copy holds: the declaration describes the episode, and one file
        # per shard would let a reader pick a rank's view as if it were the whole.
        if getattr(state, "rank", 0) == 0:
            if written and os.path.exists(written):
                # `state.batch` is the data batch; the step is on the timestamp.
                step = int(getattr(getattr(state, "timestamp", None), "batch", 0) or 0)
                self.governor.declare(written, step=step,
                                      format="other", requires=list(FRAMEWORK_REQUIRES),
                                      caveats=DATA_CAVEAT)
            else:
                self.notes.append(
                    "composer: cannot name the artifact this save wrote; no declaration was written")
        self.governor.served(gen)
        self.saves += 1

    def fit_end(self, state, logger):
        if self.governor.rank0:
            self.governor.ending()

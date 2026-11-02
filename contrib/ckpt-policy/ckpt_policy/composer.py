"""A Composer callback, without importing composer.

Remove `CheckpointSaver` (its default is `save_interval='1ep'` and `num_checkpoints_to_keep=-1`) and add
this callback. Composer's `composer.utils.checkpoint.save_checkpoint(state, filename=...)` is public, so
the request is honoured at the next batch end.

The file is Composer's own layout, not our `gen-N`: this integration decides *when*. Retention and the
`latest-rank{rank}` pointer belonged to `CheckpointSaver`; removing it means they are yours to choose.
"""


class GovernorCallback:
    """Composer Callback-compatible by duck typing: `batch_end` and `fit_end` are the two hooks used."""

    def __init__(self, governor, filename="ep{epoch}-ba{batch}-rank{rank}"):
        self.governor = governor
        self.filename = filename
        self.saves = 0

    def batch_end(self, state, logger):
        gen = self.governor.requested()
        if not gen:
            return
        # Imported here, not at module import: the package keeps zero runtime dependencies, and this
        # module must remain importable on a machine without composer.
        from composer.utils import checkpoint as composer_checkpoint

        composer_checkpoint.save_checkpoint(state, filename=self.filename)
        self.governor.served(gen)
        self.saves += 1

    def fit_end(self, state, logger):
        self.governor.ending()

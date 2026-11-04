"""A Composer callback, without importing composer.

    cb = GovernorCallback(g, latest_filename="latest-rank{rank}", num_checkpoints_to_keep=2)
    trainer = Trainer(..., callbacks=[cb])

`composer.utils.checkpoint.save_checkpoint` is public, so the request is honoured at the next batch
end. We pass the two arguments `CheckpointSaver` used to own — `num_checkpoints_to_keep` and
`latest_filename` — so retention and the `latest-rank{rank}` pointer survive.

If the installed Composer version does not accept them, the callback falls back to the older call and
records the reason in `cb.notes`; it never writes to stdout, and it never pretends the pointer was
written.
"""


class GovernorCallback:
    """Composer Callback-compatible by duck typing: `batch_end` and `fit_end` are the two hooks used."""

    def __init__(self, governor, filename="ep{epoch}-ba{batch}-rank{rank}",
                 num_checkpoints_to_keep=None, latest_filename="latest-rank{rank}"):
        self.governor = governor
        self.filename = filename
        self.num_checkpoints_to_keep = num_checkpoints_to_keep
        self.latest_filename = latest_filename
        self.notes = []
        self.saves = 0

    def batch_end(self, state, logger):
        gen = self.governor.requested()
        if not gen:
            return
        # Imported here, not at module import: the package keeps zero runtime dependencies, and this
        # module must remain importable on a machine without composer.
        from composer.utils import checkpoint as composer_checkpoint

        kwargs = {"filename": self.filename}
        if self.num_checkpoints_to_keep is not None:
            kwargs["num_checkpoints_to_keep"] = self.num_checkpoints_to_keep
        if self.latest_filename:
            rank = getattr(state, "rank", 0)
            kwargs["latest_filename"] = self.latest_filename.replace("{rank}", str(rank))
        try:
            composer_checkpoint.save_checkpoint(state, **kwargs)
        except TypeError:
            # The public function's signature grew these two arguments over time. Falling back is
            # honest: the write happens, and the caller is told which guarantee it has to manage.
            composer_checkpoint.save_checkpoint(state, filename=self.filename)
            self.notes.append(
                "composer: this version's save_checkpoint does not accept "
                "num_checkpoints_to_keep/latest_filename; retention and the latest pointer are yours")
        self.governor.served(gen)
        self.saves += 1

    def fit_end(self, state, logger):
        self.governor.ending()

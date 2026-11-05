#!/usr/bin/env python3
"""The real Composer run: the framework's own saver, driven by the policy.

    make composer        # needs torch and mosaicml
    make composer PYTHON=/path/to/venv/bin/python

The contract test in `make py` runs without composer installed. This is the run that puts the claims
on screen, on a tiny random model and synthetic tensors — no hub.
"""

import contextlib
import glob
import io
import os
import sys
import tempfile

try:
    import torch
    from torch import nn
    from torch.utils.data import DataLoader, Dataset
except ImportError as exc:  # pragma: no cover - the guard is the only path without torch
    print(f"skipped: torch not installed ({exc})")
    raise SystemExit(0)

try:
    import composer
    from composer import Trainer
    from composer.models import ComposerModel
except ImportError:  # pragma: no cover - the guard is the only path without composer
    print("skipped: composer not installed")
    raise SystemExit(0)


class TinyModel(ComposerModel):
    def __init__(self, dim=8):
        super().__init__()
        self.net = nn.Sequential(nn.Linear(dim, dim), nn.ReLU(), nn.Linear(dim, 1))

    def forward(self, batch):
        x, _y = batch
        return self.net(x)

    def loss(self, outputs, batch):
        _x, y = batch
        return nn.functional.mse_loss(outputs, y)


class TinyDataset(Dataset):
    def __init__(self, size, dim=8):
        self.size, self.dim = size, dim
        self.x = torch.randn(size, dim)
        self.y = torch.randn(size, 1)

    def __len__(self):
        return self.size

    def __getitem__(self, index):
        return self.x[index], self.y[index]


def main():
    batches = 1000
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "contrib", "ckpt-policy"))
    from ckpt_policy import Governor
    from ckpt_policy.composer import GovernorCallback

    out = tempfile.mkdtemp(prefix="composer-policy-")
    g = Governor(out, start_engine=True, engine_every="100ms", engine_window="200ms")
    try:
        cb = GovernorCallback(g)
        trainer = Trainer(
            model=TinyModel(),
            train_dataloader=DataLoader(TinyDataset(batches), batch_size=8),
            max_duration=f"{batches}ba",
            save_folder=out,
            save_interval=lambda state, event: False,   # the saver stays; its schedule is off
            save_num_checkpoints_to_keep=2,
            save_latest_filename="latest-rank{rank}.pt",
            callbacks=[cb],
            device="cpu",
            progress_bar=False,
            log_to_console=False,
            seed=7,
        )
        with contextlib.redirect_stdout(io.StringIO()):
            trainer.fit()
        final = os.path.join(out, "final-rank0.pt")
        trainer.save_checkpoint(final)     # the job's own end write

        files = sorted(p for p in glob.glob(os.path.join(out, "ep*-rank*"))
                       if not os.path.islink(p) and not os.path.basename(p).startswith("final"))
        latest = ""
        link = os.path.join(out, "latest-rank0.pt")
        if os.path.exists(link):
            latest = os.path.basename(os.path.realpath(link))
        elif files:
            latest = os.path.basename(files[-1])
        retention = "2" if not cb.notes else "yours (no CheckpointSaver was found)"
        print(f"writes {len(files)} · retention {retention} · latest {latest or '(none)'}")
        for note in cb.notes:
            print(f"  note        {note}")
        print()
        print("the point: every write went through Composer's own save_checkpoint at the batch "
              "end, and the declaration beside the artifact is what a restore is judged against")
    finally:
        g.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
"""The real Lightning run: the framework's own saver, driven by the policy.

    make lightning        # needs torch and lightning
    make lightning PYTHON=/path/to/venv/bin/python

The contract test in `make py` runs without lightning installed. This is the run that puts the
retention and latest-pointer claims on screen, on a tiny random model and synthetic tensors — no hub.
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
    import lightning as L
    from lightning.pytorch.callbacks import ModelCheckpoint
except ImportError:  # pragma: no cover - the guard is the only path without lightning
    print("skipped: lightning not installed")
    raise SystemExit(0)


class TinyModel(L.LightningModule):
    def __init__(self, dim=8):
        super().__init__()
        self.net = nn.Sequential(nn.Linear(dim, dim), nn.ReLU(), nn.Linear(dim, 1))

    def training_step(self, batch, _batch_idx):
        x, y = batch
        loss = nn.functional.mse_loss(self.net(x), y)
        self.log("train_loss", loss, prog_bar=False)
        return loss

    def configure_optimizers(self):
        return torch.optim.SGD(self.parameters(), lr=0.01)


class TinyDataset(Dataset):
    def __init__(self, size, dim=8):
        self.size, self.dim = size, dim
        self.x = torch.randn(size, dim)
        self.y = torch.randn(size, 1)

    def __len__(self):
        return self.size

    def __getitem__(self, index):
        return self.x[index], self.y[index]


def checkpoints(out_dir):
    """The copies the saver kept. `last.ckpt` is a symlink to one of them, not a third copy."""
    return sorted(p for p in glob.glob(os.path.join(out_dir, "*.ckpt"))
                  if os.path.basename(p) not in ("final.ckpt", "last.ckpt"))


def main():
    steps = 1000
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "contrib", "ckpt-policy"))
    from ckpt_policy import Governor
    from ckpt_policy.lightning import GovernorCallback

    out = tempfile.mkdtemp(prefix="lightning-policy-")
    g = Governor(out, start_engine=True, engine_every="100ms", engine_window="200ms")
    try:
        ckpt = ModelCheckpoint(
            dirpath=out,
            monitor="train_loss",
            save_top_k=2,
            save_last=True,                # keeps `last_model_path` alive; it is the framework's
            every_n_train_steps=None,      # no schedule of its own
            every_n_epochs=None,
            save_on_train_epoch_end=False,
        )
        trainer = L.Trainer(
            max_steps=steps,
            accelerator="cpu",
            devices=1,
            default_root_dir=out,
            logger=False,
            enable_progress_bar=False,
            callbacks=[GovernorCallback(g, saver=ckpt), ckpt],
        )
        with contextlib.redirect_stdout(io.StringIO()):
            trainer.fit(TinyModel(), DataLoader(TinyDataset(steps), batch_size=8))
        final = os.path.join(out, "final.ckpt")
        trainer.save_checkpoint(final)     # the job's own end write

        ckpts = checkpoints(out)
        latest = os.path.basename(ckpt.last_model_path) if ckpt.last_model_path else "(none)"
        print(f"writes {len(ckpts)} · retention 2 · latest {latest}")
        print(f"  dir         {out}")
        print(f"  end write   {os.path.basename(final)} ({os.path.exists(final)})")
        print(f"  best        {os.path.basename(ckpt.best_model_path) if ckpt.best_model_path else '(none)'}")
        print()
        print("the point: every write went through the ModelCheckpoint you kept, so save_top_k, "
              "last_model_path and best_model_path kept working")
    finally:
        g.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

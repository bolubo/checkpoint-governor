#!/usr/bin/env python3
"""The real Trainer run: the framework's cadence against the policy, on a tiny CPU model.

    make hf        # needs torch and transformers

The contract test in `make py` is torch-free; this is the run a person does once to see the numbers on
their own machine. It prints the checkpoint directories each arm left, which is the count that matters.
"""

import glob
import os
import sys
import tempfile

try:
    import torch
    from torch import nn
    from transformers import Trainer, TrainingArguments
except ImportError as exc:  # pragma: no cover - the guard is the only path without torch
    print("make hf needs torch and transformers: pip install torch transformers", file=sys.stderr)
    print(f"  ({exc})", file=sys.stderr)
    sys.exit(2)


class TinyModel(nn.Module):
    def __init__(self, vocab=16, dim=8, classes=2):
        super().__init__()
        self.emb = nn.Embedding(vocab, dim)
        self.head = nn.Linear(dim, classes)

    def forward(self, input_ids=None, labels=None, **kwargs):
        logits = self.head(self.emb(input_ids).mean(dim=1))
        loss = None
        if labels is not None:
            loss = nn.functional.cross_entropy(logits, labels)
        return {"loss": loss, "logits": logits}


class TinyDataset(torch.utils.data.Dataset):
    def __init__(self, size, length=6, vocab=16):
        self.size, self.length, self.vocab = size, length, vocab

    def __len__(self):
        return self.size

    def __getitem__(self, index):
        ids = torch.randint(0, self.vocab, (self.length,))
        return {"input_ids": ids, "labels": torch.randint(0, 2, (1,)).squeeze(0)}


def collate(batch):
    return {
        "input_ids": torch.stack([b["input_ids"] for b in batch]),
        "labels": torch.stack([b["labels"] for b in batch]),
    }


def checkpoints(out_dir):
    return sorted(glob.glob(os.path.join(out_dir, "checkpoint-*")))


def arm_cadence(steps):
    """The framework default: save every 500 steps."""
    out = tempfile.mkdtemp(prefix="hf-cadence-")
    args = TrainingArguments(
        output_dir=out, max_steps=steps, save_steps=500, logging_steps=50,
        report_to=[], use_cpu=True, save_safetensors=False,
    )
    trainer = Trainer(model=TinyModel(), args=args, train_dataset=TinyDataset(steps),
                      data_collator=collate)
    trainer.train()
    return out, checkpoints(out)


def arm_policy(steps):
    """The policy: the framework's cadence off, the callback on, one end write."""
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "contrib", "ckpt-policy"))
    from ckpt_policy import Governor
    from ckpt_policy.hf import GovernorCallback

    out = tempfile.mkdtemp(prefix="hf-policy-")
    g = Governor(out, start_engine=True, engine_every="200ms")
    try:
        args = TrainingArguments(
            output_dir=out, max_steps=steps, save_strategy="no", logging_steps=10,
            report_to=[], use_cpu=True, save_safetensors=False,
        )
        trainer = Trainer(model=TinyModel(), args=args, train_dataset=TinyDataset(steps),
                          data_collator=collate, callbacks=[GovernorCallback(g)])
        trainer.train()
        final = os.path.join(out, "final")
        trainer.save_model(final)      # the job's own end write; weights only (see the page)
        return out, checkpoints(out), final
    finally:
        g.close()


def main():
    steps = 1000
    print(f"a {steps}-step CPU job (tiny model, no shards on disk)")
    print()

    cadence_dir, cadence = arm_cadence(steps)
    policy_dir, policy, final = arm_policy(steps)

    print(f"framework default (save every 500 steps)   {len(cadence)} checkpoint(s)   {cadence_dir}")
    print(f"ckpt-policy, unconfigured                  {len(policy)} checkpoint(s)   {policy_dir}")
    print(f"end write                                   {os.path.isdir(final)}             {final}")
    print()
    print("the point: the policy arm wrote no checkpoints while training, because nothing was at risk")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

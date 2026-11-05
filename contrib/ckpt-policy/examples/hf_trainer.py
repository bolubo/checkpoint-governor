#!/usr/bin/env python3
"""A single-GPU Hugging Face Trainer with the governor, complete and copyable.

    python3 examples/hf_trainer.py /tmp/ckpt-demo

This is the S1 path from the user runs: the Trainer keeps its own saver, its periodic alarm is turned
off, and the callback asks the policy at every logging boundary. With no window (`engine_window`
unset), the job writes nothing while training and one checkpoint at the end.

The only glue beyond a normal Trainer is the marked block: two imports, the Governor, the callback,
the four TrainingArguments fields that move the alarm, `eval_dataset` (required by
`load_best_model_at_end`), and `g.close()`.
"""

import glob
import sys
import time

import torch
from torch import nn
from transformers import Trainer, TrainingArguments
from transformers.utils import logging as hf_logging

from ckpt_policy import Governor
from ckpt_policy.hf import GovernorCallback  # noqa: F401 - the marked block

hf_logging.set_verbosity_error()

JOB = sys.argv[1] if len(sys.argv) > 1 else "/tmp/ckpt-demo"
STEPS = int(sys.argv[2]) if len(sys.argv) > 2 else 200


class TinyModel(nn.Module):
    def __init__(self, vocab=16, dim=8, classes=2):
        super().__init__()
        self.emb = nn.Embedding(vocab, dim)
        self.head = nn.Linear(dim, classes)

    def forward(self, input_ids=None, labels=None, **kwargs):
        logits = self.head(self.emb(input_ids).mean(dim=1))
        loss = nn.functional.cross_entropy(logits, labels) if labels is not None else None
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


def main() -> int:
    # -- the marked block: the whole governor glue --------------------------------------------
    g = Governor(JOB, start_engine=True)
    # -----------------------------------------------------------------------------------------
    try:
        args = TrainingArguments(
            output_dir=JOB,
            max_steps=STEPS,
            save_strategy="steps",
            save_steps=10_000,              # a step the run never reaches: the alarm is off
            save_total_limit=2,             # retention stays the framework's
            eval_strategy="steps",
            eval_steps=10,                  # a request is honoured at the next logging step
            load_best_model_at_end=True,
            enable_jit_checkpoint=True,     # HF's own SIGTERM save
            logging_steps=10,
            report_to=[],
        )
        trainer = Trainer(model=TinyModel(), args=args,
                          train_dataset=TinyDataset(20_000),
                          eval_dataset=TinyDataset(200),
                          data_collator=collate,
                          callbacks=[GovernorCallback(g)])
        started = time.time()
        trainer.train()
        final = f"{JOB}/final"
        trainer.save_model(final)           # optional weights-only copy for inference
        print(f"trained {STEPS} steps in {time.time() - started:.1f}s")
        print(f"checkpoints: {len(glob.glob(JOB + '/checkpoint-*'))}")
        print(f"next: ckptctl resumable --dir={JOB}")
    finally:
        g.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

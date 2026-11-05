#!/usr/bin/env python3
"""Calibrate the disk estimate: measure a real checkpoint's bytes per parameter.

    make calibrate        # needs torch and transformers; downloads a tiny hub model

`ckptctl init --params` estimates 18 bytes per parameter (fp16 weights, two Adam moments, gradient
and master weights). That is a stated estimate, not a measurement. This trains a real model for a
few steps with AdamW, saves through the Trainer, and divides the bytes on disk by the parameter
count — so the two numbers can be compared instead of trusted.
"""

import contextlib
import io
import os
import sys
import tempfile

MODEL = os.environ.get("HF_MODEL", "hf-internal-testing/tiny-random-gpt2")
STEPS = 10

try:
    import torch
    from torch.utils.data import Dataset
    from transformers import AutoModelForCausalLM, Trainer, TrainingArguments
    from transformers.utils import logging as hf_logging
except ImportError as exc:  # pragma: no cover - the guard is the only path without torch
    print(f"make calibrate needs torch and transformers: {exc}", file=sys.stderr)
    raise SystemExit(2)

hf_logging.set_verbosity_error()


class Tokens(Dataset):
    def __init__(self, size, vocab, length=8):
        self.size, self.vocab, self.length = size, vocab, length

    def __len__(self):
        return self.size

    def __getitem__(self, index):
        ids = torch.randint(1, self.vocab, (self.length,))
        return {"input_ids": ids, "labels": ids.clone()}


def collate(batch):
    return {"input_ids": torch.stack([b["input_ids"] for b in batch]),
            "labels": torch.stack([b["labels"] for b in batch])}


def dir_bytes(path):
    total = 0
    for root, _dirs, files in os.walk(path):
        for name in files:
            total += os.path.getsize(os.path.join(root, name))
    return total, sorted(os.listdir(path))


def main():
    model = AutoModelForCausalLM.from_pretrained(MODEL)
    params = sum(p.numel() for p in model.parameters())
    vocab = model.config.vocab_size
    print(f"model {MODEL} · {params:,} parameters")

    out = tempfile.mkdtemp(prefix="calibrate-")
    args = TrainingArguments(
        output_dir=out, max_steps=STEPS, save_strategy="steps", save_steps=STEPS,
        logging_steps=STEPS, learning_rate=1e-3, report_to=[], use_cpu=True,
    )
    trainer = Trainer(model=model, args=args, train_dataset=Tokens(STEPS, vocab),
                      data_collator=collate)
    with contextlib.redirect_stdout(io.StringIO()):
        trainer.train()

    ckpt = os.path.join(out, f"checkpoint-{STEPS}")
    size, files = dir_bytes(ckpt)
    measured = size / params
    estimate = 18.0
    delta = (measured - estimate) / estimate * 100
    print(f"checkpoint {size:,} bytes · files {', '.join(files)}")
    print(f"measured {measured:.2f} B/param vs estimated {estimate:.0f} B/param ({delta:+.1f}% Δ)")
    print(f"dir {ckpt}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

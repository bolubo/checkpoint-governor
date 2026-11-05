#!/usr/bin/env python3
"""The real-model resume drill: a hub model, a Trainer checkpoint, a second process that loads it.

    make hf-resume        # needs torch, transformers, and network for the model
    HTTPS_PROXY=http://127.0.0.1:7890 make hf-resume    # when the hub needs the proxy

This is not a contract test and not part of CI: it downloads real weights and exercises the path a
person actually takes — save with the Trainer, exit, start again with `resume_from_checkpoint`, and
report what was adopted. The declaration comes from the integration's own `on_save`.
"""

import contextlib
import io
import os
import sys
import tempfile
import time

MODEL = os.environ.get("HF_MODEL", "hf-internal-testing/tiny-random-gpt2")
STEPS = 50

try:
    import torch
    from torch.utils.data import Dataset
    from transformers import (AutoModelForCausalLM, AutoTokenizer, Trainer,
                              TrainingArguments)
    from transformers.utils import logging as hf_logging
except ImportError as exc:  # pragma: no cover - the guard is the only path without torch
    print(f"make hf-resume needs torch and transformers: {exc}", file=sys.stderr)
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


def main():
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "contrib", "ckpt-policy"))
    from ckpt_policy import Governor
    from ckpt_policy.hf import GovernorCallback

    out = tempfile.mkdtemp(prefix="hf-resume-")
    print(f"model {MODEL} · {STEPS} steps · {out}")

    tok = AutoTokenizer.from_pretrained(MODEL)
    if tok.pad_token is None:
        tok.pad_token = tok.eos_token
    model = AutoModelForCausalLM.from_pretrained(MODEL)
    vocab = model.config.vocab_size

    g = Governor(out)
    args = TrainingArguments(
        output_dir=out, max_steps=STEPS, save_strategy="steps", save_steps=STEPS,
        save_total_limit=2, logging_steps=10, report_to=[], use_cpu=True,
    )
    trainer = Trainer(model=model, args=args, train_dataset=Tokens(STEPS, vocab),
                      data_collator=collate, callbacks=[GovernorCallback(g)])
    with contextlib.redirect_stdout(io.StringIO()):
        trainer.train()
    ckpt = os.path.join(out, f"checkpoint-{STEPS}")
    if not os.path.isdir(ckpt):
        print(f"FAIL: the Trainer did not leave {ckpt}", file=sys.stderr)
        return 1
    print(f"saved {os.path.basename(ckpt)} (step {trainer.state.global_step})")

    resumed = ckptctl_check(out, "resumable")
    print(f"resumable: {resumed}")
    if not resumed.startswith("resumable"):
        print(f"FAIL: resumable said {resumed}", file=sys.stderr)
        return 1

    # A separate model, as a new process would build it. The load time is measured on its own, so
    # `load-seconds` is a load and not a load plus two training steps.
    second = AutoModelForCausalLM.from_pretrained(MODEL)
    started = time.perf_counter()
    torch.load(os.path.join(ckpt, "optimizer.pt"), map_location="cpu", weights_only=False)
    load_s = time.perf_counter() - started

    # A new process: a new Governor, which stamps a new boot time. The copy it may adopt has to be
    # older than that stamp; a checkpoint this resumed run writes itself must not look "unused".
    g2 = Governor(out)
    args2 = TrainingArguments(
        output_dir=out, max_steps=STEPS + 2, save_strategy="steps", save_steps=STEPS + 2,
        save_total_limit=2, logging_steps=10, report_to=[], use_cpu=True,
    )
    trainer2 = Trainer(model=second, args=args2, train_dataset=Tokens(STEPS + 2, vocab),
                       data_collator=collate, callbacks=[GovernorCallback(g2)])
    with contextlib.redirect_stdout(io.StringIO()):
        trainer2.train(resume_from_checkpoint=ckpt)
    adopted = trainer2.state.global_step
    print(f"loaded in {load_s:.2f}s · global_step {STEPS} → {adopted}")

    g2.resumed(STEPS, STEPS, seconds=load_s)
    report = ckptctl_check(out, "resumed")
    print(f"glue resumed-step {report_step(out)} · load-seconds {load_s:.3f}")
    print(f"resumed: {report}")
    return 0


def report_step(out):
    try:
        with open(os.path.join(out, ".ckpt-governor", "resumed-step"), encoding="utf-8") as f:
            return f.read().strip()
    except OSError:
        return "(missing)"


def ckptctl_check(out, command):
    import subprocess
    engine = os.path.join(os.path.dirname(__file__), "..", "contrib", "ckpt-policy",
                          "ckpt_policy", "_bin", "ckptctl")
    proc = subprocess.run([engine, command, "--dir", out], capture_output=True, text=True)
    return (proc.stdout + proc.stderr).strip()


if __name__ == "__main__":
    raise SystemExit(main())

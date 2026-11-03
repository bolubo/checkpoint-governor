#!/usr/bin/env python3
"""S2: one trial of the announced-stop chain, with a real model and a real signal.

Two arms:

  us            the governor is in the room, `no-us=none`, no periodic writes. The plan decides; the
                job writes only when the plan asks, and waits for `may-exit` inside its grace.
  default500    the framework's own cadence and nothing else: a write every 500 steps, and no signal
                handler. This is what the counterfactual costs.

The runner sends SIGTERM at `--signal-at`. Everything is recorded in `job.json` (timeline, saves,
steps), so the score — margin before the deadline, lost steps, whether the copy verifies — is computed
from facts rather than reconstructed from logs.

    python s2_job.py --dir=/root/tmp/s2/us-30s-1s-1 --arm=us --grace=30 --every=1s --signal-at=20
"""

import argparse
import json
import os
import signal
import sys
import time

import torch
from transformers import AutoModelForCausalLM, AutoTokenizer

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "contrib", "ckpt-policy"))
from ckpt_policy import Governor, Saves, World  # noqa: E402


def atomic_json(path: str, obj: dict) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(obj, f, indent=1)
    os.replace(tmp, path)


def fsync_path(p: str) -> None:
    with open(p, "rb") as f:
        os.fsync(f.fileno())


def write_text(root: str, name: str, value: str) -> None:
    d = os.path.join(root, ".ckpt-governor")
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, name), "w") as f:
        f.write(value)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--arm", choices=["us", "default500"], default="us")
    ap.add_argument("--model", default="experiments/real/models/Qwen2.5-0.5B")
    ap.add_argument("--data", default="experiments/real/data/wikitext2.parquet")
    ap.add_argument("--seq", type=int, default=512)
    ap.add_argument("--batch", type=int, default=1)
    ap.add_argument("--steps", type=int, default=600, help="steps to run when nothing interrupts")
    ap.add_argument("--save-every", type=int, default=500, help="the default500 arm's cadence")
    ap.add_argument("--grace", type=int, default=30)
    ap.add_argument("--wait-after-seal", type=float, default=2.0,
                    help="how long to keep waiting for may-exit after the copy is sealed")
    ap.add_argument("--every", default="5s", help="the engine's round cadence")
    ap.add_argument("--window", default=None)
    ap.add_argument("--estimate", default=None,
                    help="declare one write's duration to the engine (e.g. 20s); the no-time door uses it")
    ap.add_argument("--dtype", default="float32", choices=["float32", "bfloat16"])
    ap.add_argument("--weights-only", action="store_true",
                    help="save the model only, to measure the chain as a function of S")
    a = ap.parse_args()

    root = a.dir
    os.makedirs(root, exist_ok=True)
    job_path = os.path.join(root, "job.json")
    dtype = torch.float32 if a.dtype == "float32" else torch.bfloat16

    tokenizer = AutoTokenizer.from_pretrained(a.model)
    try:
        model = AutoModelForCausalLM.from_pretrained(a.model, dtype=dtype)
    except TypeError:
        model = AutoModelForCausalLM.from_pretrained(a.model, torch_dtype=dtype)
    model.to("cuda")
    model.train()
    opt = torch.optim.AdamW(model.parameters(), lr=1e-4)

    ids = None
    if os.path.exists(a.data):
        try:
            import pyarrow.parquet as pq

            text = "\n".join(t for t in pq.read_table(a.data, columns=["text"]).column("text").to_pylist() if t and t.strip())
            ids = tokenizer(text, return_tensors="pt", add_special_tokens=False).input_ids
        except Exception:  # noqa: BLE001 - random ids are the documented fallback
            ids = None
    if ids is None:
        ids = torch.randint(0, tokenizer.vocab_size, (a.seq * a.batch * (a.steps + 2),))
    while ids.numel() < a.seq * a.batch * (a.steps + 2):
        ids = ids.repeat(1, 2)

    rec = {
        "arm": a.arm, "grace": a.grace, "every": a.every, "window": a.window,
        "save_interval": a.save_every if a.arm == "default500" else None,
        "weights_only": a.weights_only,
        "t_start": time.monotonic(), "t_signal": None, "t_request_seen": None,
        "t_exit": None, "steps_at_signal": None, "steps_done": 0,
        "saves": [], "exit": None,
    }
    atomic_json(job_path, rec)

    gov = None
    if a.arm == "us":
        args = ["--no-us", "none"]
        if a.estimate:
            args += ["--estimate", a.estimate]
        gov = Governor(root, world=World(ranks=1), saves=Saves(), start_engine=True,
                       engine_every=a.every, engine_window=a.window,
                       engine_args=args)

    def plan_now() -> dict:
        """The plan's own words at this moment: the reason the claim is about."""
        try:
            with open(os.path.join(root, ".ckpt-governor", "state.json")) as f:
                p = json.load(f)
            return {"action": p.get("action"), "reason": p.get("reason"),
                    "reasons": [r.get("code") for r in p.get("reasons") or []],
                    "missing": p.get("missing") or []}
        except Exception:  # noqa: BLE001 - a plan that cannot be read is not a plan
            return {}

    stopping = {"at": None}

    def on_term(*_):
        if stopping["at"] is None:
            stopping["at"] = time.monotonic()
            rec["t_signal"] = stopping["at"]
            rec["steps_at_signal"] = rec["steps_done"]
            if gov is not None:
                gov.on_sigterm(a.grace)
            atomic_json(job_path, rec)

    signal.signal(signal.SIGTERM, on_term)

    def state():
        if a.weights_only:
            return {"model": model.state_dict()}
        return {"model": model.state_dict(), "optimizer": opt.state_dict()}

    def save(step: int, trigger: str) -> None:
        t0 = time.monotonic()
        if gov is not None:
            gen = gov.generation()
            d = os.path.join(root, f"gen-{gen:04d}")
            os.makedirs(d, exist_ok=True)
            p = os.path.join(d, "rank-0.pt")
            torch.save(state(), p)
            fsync_path(p)
            fd = os.open(d, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
            t_seal = time.monotonic()
            gov.sealed(gen, step, ["rank-0.pt"])
            seal_s = time.monotonic() - t_seal
            name = f"gen-{gen:04d}"
        else:
            d = os.path.join(root, f"step-{step}")
            os.makedirs(d, exist_ok=True)
            p = os.path.join(d, "rank-0.pt")
            torch.save(state(), p)
            fsync_path(p)
            name = f"step-{step}"
            seal_s = 0.0
        elapsed = time.monotonic() - t0
        rec["saves"].append({
            "name": name, "step": step, "trigger": trigger,
            "t_start": t0, "t_end": t0 + elapsed, "seal_s": seal_s,
            "bytes": os.path.getsize(p),
        })
        # The job knows what its own write cost; the engine's no-time door can only use a number
        # somebody published. `last-save-seconds` is that number, and it is the contract's file (for
        # the same reason the trainer writes it): the decision is the engine's, the measurement is the
        # job's.
        write_text(root, "last-save-seconds", f"{elapsed:.3f}")
        atomic_json(job_path, rec)

    def batch(i: int):
        start = i * a.seq * a.batch
        return ids[0, start:start + a.seq * a.batch].view(a.batch, a.seq).to("cuda")

    step = 0
    try:
        while step < a.steps:
            step += 1
            x = batch(step)
            loss = model(input_ids=x, labels=x).loss
            opt.zero_grad(set_to_none=True)
            loss.backward()
            opt.step()
            rec["steps_done"] = step
            if gov is not None:
                gov.step(step)
                if gov.should_save():
                    if rec["t_request_seen"] is None and stopping["at"] is not None:
                        rec["t_request_seen"] = time.monotonic()
                        rec["reason_at_request"] = plan_now()
                    save(step, "request" if stopping["at"] else "plan")
                if stopping["at"] is not None:
                    rec["exit"] = "step loop stopped on the signal"
                    break
            elif step % a.save_every == 0:
                save(step, "cadence")
            if stopping["at"] is not None and gov is None:
                rec["exit"] = "signal, no handler"
                break

        while stopping["at"] is not None and gov is not None:
            if rec["t_request_seen"] is None and gov.should_save():
                rec["t_request_seen"] = time.monotonic()
                rec["reason_at_request"] = plan_now()
            if gov.should_save():
                save(rec["steps_done"], "request")
            rec["may_exit"] = gov.may_exit()
            after_signal = [s for s in rec["saves"] if s["t_end"] > stopping["at"]]
            if rec["may_exit"]:
                rec["exit"] = "may-exit"
                break
            # A sealed copy inside the grace is the thing the claim is about; waiting out the rest of
            # the grace afterwards is the platform's business, not ours.
            if after_signal and (time.monotonic() - after_signal[-1]["t_end"]) > a.wait_after_seal:
                rec["exit"] = "sealed, no may-exit"
                break
            if (time.monotonic() - stopping["at"]) >= a.grace:
                rec["exit"] = "grace expired"
                break
            time.sleep(0.05)
        if rec["exit"] is None:
            rec["exit"] = "finished"
    finally:
        rec["t_exit"] = time.monotonic()
        atomic_json(job_path, rec)
        if gov is not None:
            gov.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())

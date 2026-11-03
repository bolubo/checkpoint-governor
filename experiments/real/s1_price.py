#!/usr/bin/env python3
"""S1: what one real checkpoint costs on this machine.

The chain budget is measured in seconds, so the first thing to measure is the term that is not the
controller's: how long a real state takes to land. This loads a real model, takes a few real training
steps on real text, and prints three numbers — `t_step`, `S` (model + optimizer), `T_write` — plus the
three quantities the budget is made of (write in steps, waste at the framework's default interval, and
the free space `store.low-space` asks for).

    experiments/real/venv/bin/python experiments/real/s1_price.py

Hugging Face is not reachable from this network, so `HF_ENDPOINT` defaults to the mirror; override it
with the environment variable when the real hub is reachable. Results go to
`experiments/real/results/s1-price.json`, which is generated and therefore not committed.
"""

import argparse
import json
import os
import statistics
import subprocess
import sys
import time

os.environ.setdefault("HF_ENDPOINT", os.environ.get("HF_ENDPOINT", "https://hf-mirror.com"))
os.environ.setdefault("HF_HUB_DISABLE_TELEMETRY", "1")

import torch  # noqa: E402
from transformers import AutoModelForCausalLM, AutoTokenizer  # noqa: E402

GIB = 2 ** 30


def gpu_line() -> str:
    try:
        return subprocess.run(
            ["nvidia-smi", "--query-gpu=temperature.gpu,clocks.sm,power.draw,memory.used",
             "--format=csv,noheader"],
            capture_output=True, text=True, timeout=10,
        ).stdout.strip()
    except Exception:  # noqa: BLE001 - a missing tool is not a failure of the measurement
        return "unavailable"


def batches(tokenizer, data_path: str | None, seq_len: int, batch: int, count: int):
    """Real text when the parquet is there, random ids otherwise. Returns (generator, source)."""
    ids = None
    source = "random-token-ids"
    if data_path and os.path.exists(data_path):
        try:
            import pyarrow.parquet as pq

            table = pq.read_table(data_path, columns=["text"])
            text = "\n".join(t for t in table.column("text").to_pylist() if t and t.strip())
            ids = tokenizer(text, return_tensors="pt", add_special_tokens=False).input_ids
            source = f"{os.path.basename(data_path)} ({len(text)} chars)"
        except Exception as e:  # noqa: BLE001 - the fallback is part of the contract
            print(f"dataset fallback ({type(e).__name__}): {e}")
    if ids is None:
        ids = torch.randint(0, tokenizer.vocab_size, (seq_len * batch * (count + 2),))

    need = seq_len * batch * (count + 2)
    if ids.numel() < need:
        repeat = (need // ids.numel()) + 1
        ids = ids.repeat(1, repeat)

    def gen():
        for i in range(count):
            start = i * seq_len * batch
            chunk = ids[0, start:start + seq_len * batch].view(batch, seq_len)
            yield chunk

    return gen(), source


def tensor_bytes(x) -> int:
    """Every tensor inside a nested dict/list, which is what a checkpoint really writes."""
    if torch.is_tensor(x):
        return x.numel() * x.element_size()
    if isinstance(x, dict):
        return sum(tensor_bytes(v) for v in x.values())
    if isinstance(x, (list, tuple)):
        return sum(tensor_bytes(v) for v in x)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="experiments/real/models/Qwen2.5-0.5B",
                    help="a local directory; run experiments/real/fetch.py first")
    ap.add_argument("--data", default="experiments/real/data/wikitext2.parquet")
    ap.add_argument("--seq", type=int, default=512)
    ap.add_argument("--batch", type=int, default=1)
    ap.add_argument("--steps", type=int, default=30, help="timed steps, after 5 warm-up steps")
    ap.add_argument("--writes", type=int, default=3)
    ap.add_argument("--dtype", default="float32", choices=["float32", "bfloat16"])
    ap.add_argument("--out", default="experiments/real/results")
    a = ap.parse_args()

    if not torch.cuda.is_available():
        print("S1 needs a GPU: torch.cuda.is_available() is False", file=sys.stderr)
        return 2

    if not os.path.isdir(a.model):
        print(f"no model at {a.model}: run `python3 experiments/real/fetch.py` first", file=sys.stderr)
        return 2

    dtype = torch.float32 if a.dtype == "float32" else torch.bfloat16
    print(f"gpu before: {gpu_line()}")

    tokenizer = AutoTokenizer.from_pretrained(a.model)
    try:
        model = AutoModelForCausalLM.from_pretrained(a.model, dtype=dtype)
    except TypeError:  # transformers < 5
        model = AutoModelForCausalLM.from_pretrained(a.model, torch_dtype=dtype)
    model.to("cuda")
    model.train()

    params = sum(p.numel() for p in model.parameters())
    opt = torch.optim.AdamW(model.parameters(), lr=1e-4)
    print(f"model: {a.model}  params {params/1e6:.1f} M  dtype {a.dtype}")

    gen, source = batches(tokenizer, a.data, a.seq, a.batch, a.steps + 5)
    torch.cuda.reset_peak_memory_stats()

    # Warm-up: the first steps compile kernels and page in weights.
    for _ in range(5):
        ids = next(gen).to("cuda")
        loss = model(input_ids=ids, labels=ids).loss
        opt.zero_grad(set_to_none=True)
        loss.backward()
        opt.step()
    torch.cuda.synchronize()

    t_step = []
    for _ in range(a.steps):
        ids = next(gen).to("cuda")
        s = time.perf_counter()
        loss = model(input_ids=ids, labels=ids).loss
        opt.zero_grad(set_to_none=True)
        loss.backward()
        opt.step()
        torch.cuda.synchronize()
        t_step.append(time.perf_counter() - s)
    t_step.sort()
    step = t_step[len(t_step) // 2]
    peak = torch.cuda.max_memory_allocated() / GIB

    state = {"model": model.state_dict(), "optimizer": opt.state_dict()}
    tensors = tensor_bytes(state)

    os.makedirs(a.out, exist_ok=True)
    state_dir = os.path.join(a.out, "state-s1")
    os.makedirs(state_dir, exist_ok=True)
    path = os.path.join(state_dir, "state.pt")
    t_write = []
    for _ in range(a.writes):
        s = time.perf_counter()
        torch.save(state, path)
        with open(path, "rb") as f:
            os.fsync(f.fileno())
        dfd = os.open(state_dir, os.O_RDONLY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)
        t_write.append(time.perf_counter() - s)
    t_write.sort()
    write = t_write[len(t_write) // 2]
    size = os.path.getsize(path)

    interval = 500 * step
    waste = write / interval
    free_needed = 2 * size
    derived = {
        "write_in_steps": write / step,
        "interval_at_500_steps_s": interval,
        "waste_at_500": waste,
        "free_bytes_needed": free_needed,
        "chain_5s_round_5s_probe_s": 10 + write,
        "margin_grace_30s": 30 - (10 + write),
        "margin_grace_10s": 10 - (10 + write),
        "margin_grace_5s": 5 - (10 + write),
    }

    print()
    print(f"source            {source}")
    print(f"t_step            {step*1000:.1f} ms   (median of {a.steps}, batch {a.batch} x {a.seq})")
    print(f"S                 {size/GIB:.2f} GiB ({tensors/GIB:.2f} GiB of tensors)")
    print(f"T_write           {write:.3f} s    (median of {a.writes}, file + directory fsync'd)")
    print(f"peak vram         {peak:.2f} GiB")
    print(f"write in steps    {derived['write_in_steps']:.0f} steps")
    print(f"waste @500        {waste*100:.1f}%   (T_write / (500 x t_step))")
    print(f"needs free        {free_needed/GIB:.1f} GiB (2 x S, the store.low-space trigger)")
    print(
        "chain 5s/5s       %.1f s  ->  grace 30s %+.1fs · 10s %+.1fs · 5s %+.1fs"
        % (
            derived["chain_5s_round_5s_probe_s"],
            derived["margin_grace_30s"],
            derived["margin_grace_10s"],
            derived["margin_grace_5s"],
        )
    )
    print(f"gpu after:  {gpu_line()}")

    record = {
        "stage": "s1-price",
        "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "model": a.model,
        "dtype": a.dtype,
        "seq": a.seq,
        "batch": a.batch,
        "params": params,
        "source": source,
        "t_step_s": step,
        "t_step_samples": t_step[:5],
        "state_bytes": size,
        "state_tensor_bytes": tensors,
        "t_write_s": write,
        "t_write_samples": t_write,
        "peak_vram_gib": peak,
        "derived": derived,
        "state_path": os.path.abspath(path),
    }
    with open(os.path.join(a.out, "s1-price.json"), "w") as f:
        json.dump(record, f, indent=1)
    print(f"written: {os.path.join(a.out, 's1-price.json')}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""One small GPU benchmark, on the card this machine actually has.

    make gpu-bench PYTHON=/root/torch-env/bin/python

This is not a training benchmark and not a CI gate. It answers one question a real run already asked:
does this torch build execute kernels on this GPU (sm_120), and at roughly what rate? Two kernels —
a large fp16 matmul and a memory copy — are enough to tell "works" from "falls back to CPU".
"""

import sys
import time

try:
    import torch
except ImportError as exc:  # pragma: no cover
    print(f"make gpu-bench needs torch: {exc}", file=sys.stderr)
    raise SystemExit(2)

if not torch.cuda.is_available():
    print("gpu-bench: no cuda device; nothing to measure", file=sys.stderr)
    raise SystemExit(2)


def main():
    name = torch.cuda.get_device_name(0)
    major, minor = torch.cuda.get_device_capability(0)
    print(f"device={name} · compute={major}.{minor} · torch={torch.__version__}")

    n = 4096
    a = torch.randn(n, n, device="cuda", dtype=torch.float16)
    b = torch.randn(n, n, device="cuda", dtype=torch.float16)
    for _ in range(3):  # warm up allocator and kernels
        c = a @ b
    torch.cuda.synchronize()
    runs, started = 5, time.perf_counter()
    for _ in range(runs):
        c = a @ b
    torch.cuda.synchronize()
    seconds = time.perf_counter() - started
    tflops = 2 * n ** 3 * runs / seconds / 1e12
    print(f"matmul {n}x{n} fp16: {tflops:.1f} TFLOPs over {runs} runs")

    size = 256 << 20  # 256 MiB
    x = torch.empty(size // 2, device="cuda", dtype=torch.float16)
    y = torch.empty_like(x)
    y.copy_(x)
    torch.cuda.synchronize()
    started = time.perf_counter()
    for _ in range(10):
        y.copy_(x)
    torch.cuda.synchronize()
    seconds = time.perf_counter() - started
    gbs = 2 * x.numel() * 2 * 10 / seconds / 1e9  # read + write
    print(f"copy 256 MiB: {gbs:.0f} GB/s (read + write)")
    print("gpu-bench: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

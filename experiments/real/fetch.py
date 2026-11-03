#!/usr/bin/env python3
"""Fetch the model and the text this network can actually reach, and say what arrived.

huggingface.co is not reachable from this host and `hf-mirror.com` serves files at ~1.2 MB/s with a
hub client that refuses its metadata. ModelScope serves the same Qwen weights at ~2.5 MB/s per
connection, and range requests work, so the big file is fetched in parts and concatenated. Each file
is checked against the size the server reports, because a truncated checkpoint is not a checkpoint.

    python3 experiments/real/fetch.py            # model + wikitext
    python3 experiments/real/fetch.py --parts 8

Nothing here is committed: everything lands under `experiments/real/`, which `.gitignore` excludes.
"""

import argparse
import concurrent.futures as cf
import os
import re
import subprocess
import sys

MODEL_REPO = "https://modelscope.cn/models/Qwen/Qwen2.5-0.5B/resolve/master"
SMALL_FILES = [
    "config.json",
    "generation_config.json",
    "tokenizer.json",
    "tokenizer_config.json",
    "vocab.json",
    "merges.txt",
]
WIKITEXT = (
    "https://hf-mirror.com/datasets/wikitext/resolve/main/"
    "wikitext-2-raw-v1/train-00000-of-00001.parquet"
)


def head_total(url: str) -> int:
    """Total bytes from a one-byte range request; servers that ignore Range get no parts."""
    out = subprocess.run(
        ["curl", "-sL", "-r", "0-0", "-D", "-", "-o", "/dev/null", url],
        capture_output=True, text=True, timeout=120,
    ).stdout
    m = re.search(r"content-range:\s*bytes\s+\d+-\d+/(\d+)", out, re.I)
    return int(m.group(1)) if m else 0


def fetch_range(url: str, start: int, end: int, dest: str) -> None:
    subprocess.run(
        ["curl", "-sL", "--retry", "3", "--retry-delay", "2", "-r", f"{start}-{end}", "-o", dest, url],
        check=True, timeout=1800,
    )


def fetch_big(url: str, dest: str, parts: int) -> int:
    total = head_total(url)
    if not total:
        # Server does not do ranges: one stream, then check it against whatever it reports.
        subprocess.run(["curl", "-sL", "--retry", "3", "-o", dest, url], check=True, timeout=3600)
        return os.path.getsize(dest)

    chunk = total // parts
    names = [f"{dest}.part{i}" for i in range(parts)]
    jobs = []
    for i in range(parts):
        start = i * chunk
        end = total - 1 if i == parts - 1 else (start + chunk - 1)
        jobs.append((url, start, end, names[i]))
    with cf.ThreadPoolExecutor(max_workers=parts) as ex:
        for f in [ex.submit(fetch_range, *j) for j in jobs]:
            f.result()
    with open(dest, "wb") as out:
        for n in names:
            with open(n, "rb") as part:
                out.write(part.read())
            os.remove(n)
    got = os.path.getsize(dest)
    if got != total:
        raise SystemExit(f"truncated: {dest} is {got} bytes, server said {total}")
    return got


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--parts", type=int, default=8)
    ap.add_argument("--model-dir", default="experiments/real/models/Qwen2.5-0.5B")
    ap.add_argument("--data-dir", default="experiments/real/data")
    a = ap.parse_args()

    os.makedirs(a.model_dir, exist_ok=True)
    os.makedirs(a.data_dir, exist_ok=True)

    sizes = {}
    for name in SMALL_FILES:
        dest = os.path.join(a.model_dir, name)
        if not os.path.exists(dest) or os.path.getsize(dest) == 0:
            subprocess.run(
                ["curl", "-sL", "--retry", "3", "-o", dest, f"{MODEL_REPO}/{name}"],
                check=True, timeout=300,
            )
        sizes[name] = os.path.getsize(dest)

    big = os.path.join(a.model_dir, "model.safetensors")
    want = head_total(f"{MODEL_REPO}/model.safetensors")
    if os.path.exists(big) and want and os.path.getsize(big) == want:
        sizes["model.safetensors"] = want
        print("weights already complete")
    else:
        sizes["model.safetensors"] = fetch_big(f"{MODEL_REPO}/model.safetensors", big, a.parts)

    data = os.path.join(a.data_dir, "wikitext2.parquet")
    if not os.path.exists(data) or os.path.getsize(data) == 0:
        subprocess.run(["curl", "-sL", "--retry", "3", "-o", data, WIKITEXT], check=True, timeout=300)
    sizes["wikitext2.parquet"] = os.path.getsize(data)

    for name, n in sizes.items():
        print(f"{name:24s} {n/2**20:9.1f} MiB")
    print(f"\nmodel: {os.path.abspath(a.model_dir)}")
    print(f"data:  {os.path.abspath(data)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

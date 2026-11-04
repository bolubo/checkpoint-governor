"""The two files a writer seals: the manifest (bytes and ordering) and the declaration (what a restore
would get).

We write metadata, never a shard. The shards are the user's library's job; `sealed()` hashes the files
it is given and records what they claim, exactly the way the Go writer does, so one verifier reads both.
"""

import hashlib
import json
import os

from .glue import write_atomic

TAIL_BYTES = 1 << 20
RESUME_SCHEMA = 1


def sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def tail_sha256(path: str, size: int) -> str:
    """The hash of the last min(size, 1 MiB) bytes: what the cheap check compares against a kill."""
    start = max(0, size - TAIL_BYTES)
    h = hashlib.sha256()
    with open(path, "rb") as f:
        f.seek(start)
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def build_manifest(generation: int, step: int, sealed_at: str, writer: str, shards: list[str],
                   base: str) -> dict:
    """One manifest for one generation. `shards` are paths relative to `base` (the generation dir)."""
    entries = []
    for name in shards:
        path = os.path.join(base, name)
        size = os.path.getsize(path)
        entries.append({
            "name": name,
            "size": size,
            "sha256": sha256_file(path),
            "tailSha256": tail_sha256(path, size),
        })
    return {
        "generation": generation,
        "step": step,
        "sealedAt": sealed_at,
        "writer": writer,
        "shards": entries,
    }


def write_manifest(gen_dir: str, manifest: dict) -> None:
    """Seal last: the manifest is written after every shard, through a temporary name, then renamed."""
    raw = (json.dumps(manifest, indent=2) + "\n").encode("utf-8")
    write_atomic(os.path.join(gen_dir, "manifest.json"), raw)


def write_resume(gen_dir: str, *, generation: int, step: int, format: str, world: dict,
                 state: dict, partial=None, loader=None, requires=None, target=None,
                 file_name: str = "resume.json") -> None:
    """The declaration, written beside the manifest. The reader treats it as untrusted input.

    `requires` is which states a resume of *this* job needs; absent means the reader's default five.
    `target` names the artifact the declaration describes: `"."` for the directory it sits in, or a
    sibling file name (Lightning `.ckpt`, Composer episode). Both are optional, so an older reader
    keeps its old answer.
    """
    doc = {
        "schema": RESUME_SCHEMA,
        "generation": generation,
        "step": step,
        "format": format,
        "world": {
            "ranks": int(world.get("ranks", 1)),
            "tp": int(world.get("tp", 1)),
            "pp": int(world.get("pp", 1)),
            "dp": int(world.get("dp", 1)),
            "reshardable": bool(world.get("reshardable", False)),
        },
        "state": {name: bool(state.get(name, False)) for name in
                  ("model", "optimizer", "scheduler", "rng", "dataloader", "scaler", "ema")},
    }
    if partial:
        doc["partial"] = list(partial)
    if requires:
        doc["requires"] = list(requires)
    if target:
        doc["target"] = target
    if loader:
        doc["loader"] = loader
    raw = (json.dumps(doc, indent=2) + "\n").encode("utf-8")
    write_atomic(os.path.join(gen_dir, file_name), raw)

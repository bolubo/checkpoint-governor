# v0.25.0 — installed, run for real, said exactly

This release is about three promises that were written before they were true: that a page can be
followed on a fresh machine, that the framework integrations work against a real framework, and that
a declaration says what the job needs rather than what this build happens to know.

## What the first real run found

The Lightning and Composer callbacks had passed their contract tests for a month without ever loading
the framework. The first real `Trainer` failed at `setup` — Lightning calls every hook on every
callback, and a plain object has none. Composer failed one level deeper, at `run_event`.

Fixing the first failure exposed the second: the public save functions write a file but skip the
bookkeeping the framework does around it. The Lightning run printed `best (none)` because
`save_checkpoint(trainer)` never updates `best_model_path`; Composer 0.32.1's `save_checkpoint` does
not accept `num_checkpoints_to_keep` or `latest_filename` at all. Both callbacks now subclass their
framework's callback and drive the framework's own routines — the saver's rotation and pointers stay
the framework's, which was the claim all along.

A third defect was quieter. A framework write leaves no `gen-*` on disk, so the client saw the same
generation every time; dedupe by generation silenced every window request after the first one. The
key is now `requestId`, and a test holds it.

## What it does now

**The install page is copyable on a managed Python.** `python3 -m venv .venv` comes first, in
`docs/install-python.md` and in all three integration pages, because this machine's system Python
refuses `pip install` under PEP 668. Each page opens with one `Paste this` block; the first import
is identical on all three, and a test reads them.

**Disk is priced before anything is written.**

```
$ ckptctl init --dir=/ckpt/job-1 --params=7B --duration=4h --answer=periodic
one write: unknown (nothing measured, and the job has not declared a size)
disk: 117 GiB × 1 copy (estimated from 7B, not measured; disk only, not a GPU check)

  none       nothing            0 writes    disk: 117 GiB × 1 copy   a silent death costs everything
  ...
```

The formula is 18 bytes per parameter and is printed in `--help`. The line never pretends to be a
measurement, and it is about disk, not whether the GPU can hold the model.

**The declaration says what it needs.** `ckptctl declare --world=8 --tp=4 --pp=2 --dp=1` records the
world the copy was written under, so `resumable --world=8` answers against it. `wrap` takes the
platform's `deployment.json grace_seconds` when `--grace` is absent and prints where it came from:

```
wrap: grace 60s from deployment.json
```

`status` names the `ext` keys a reader ignored — names only, never values.

**The receipt says where the copy lives.**

```
durability      local disk: a copy outlives the process, not the volume
```

## The real runs

| run | host | what it printed |
|---|---|---|
| Hugging Face | `/root/llamafactory-venv` (torch 2.13.0+cu130) | `retention 2` · `latest checkpoint-999` |
| Lightning 2.6.6 | the same venv, plus `lightning` and four small packages | `writes 1 · retention 2 · latest last.ckpt` |
| Composer 0.32.1 | `/root/tmp/sf-gpu-venv` (Python 3.12) | `writes 2 · retention 2 · latest ep2-ba317-rank0.pt` |

The Lightning and Composer runs use a random-init model and synthetic tensors: the chain being proven
is write → retention → latest → declaration, not model quality. Installing Composer 0.32.1 constrains
that venv to `torch 2.7.0+cu126`; the run is on CPU because sm_120 is outside that build's CUDA
architectures.

A Hugging Face `best` checkpoint exists only when a write lands on an evaluation boundary — the
run above printed `best (none)`, and a probe that hit the boundary printed `checkpoint-830`. Resumability
does not depend on it.

## Version skew, with real binaries

`hack/skew.sh` builds the reader from the v0.24 ref and lets it judge a declaration written for this
release:

```
old reader refused: needs ckptctl >= 0.25 (you have 0.24)
new reader accepted
```

No hand-written error string stands in for the old build.

## What this does not do

Physical-disk low-space, the 1-second grace matrix, two-node Kubernetes, real-model resume, and
hour-long soak runs are the testing round's work; they are listed there so they are not forgotten.

## Upgrade

```
pip install -U ckpt-policy
```

The wheel carries the engine; upgrade both together, never one half. `ckptctl >= 0.25` is what reads
a declaration that names `min_reader: 0.25`.

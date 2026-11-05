# Roadmap

Status: 2026-11-05, current release `v0.26.0`. The direction is decision
[0028](decisions/0028-the-direction-is-a-library.md): a policy library in the training loop, one Go
engine, grown through integrations. The default is zero periodic writes.

This page is the order of work, not a list of every test. Each item names what would make it done.

## Shipped

- **The contrast and the model.** `make contrast` prints the four policies; `make waste` prices the
  framework defaults at four scales. Both are in the README as evidence.
- **The Python front door.** The wheel carries the Go engine, `pip install` installs `ckptctl`, and
  three lines wire a loop. `python -m ckpt_policy.demo` shows the two arms.
- **Three framework integrations.** Hugging Face Trainer, Lightning and Composer; the framework's
  own saver stays, the callback asks the policy. Real runs found and fixed the callback and save-path
  defects (CHANGELOG v0.25).
- **The multi-rank path.** `RANK`/`WORLD_SIZE` are read from the launcher; rank 0 owns the engine;
  shards and the seal are explained in [multi-rank.md](multi-rank.md), with a runnable
  `examples/two_ranks.py`.
- **The gate.** `contrib/gate` holds a higher-priority arrival until the victim seals; measured at
  4.8s, lost 0 steps (decision 0031, [gate.md](gate.md)).
- **The user path.** [first-ten-minutes.md](first-ten-minutes.md), [troubleshooting.md](troubleshooting.md),
  and the two examples above.

## Next, in order

1. **A real DDP/FSDP run.** Two ranks with gradient sync, the Governor on each, one engine, `resumable`
   for the job. Done when `torchrun --nproc_per_node=2` completes and `ckptctl resumable` answers on a
   host where its rendezvous works. Not measured here: this WSL's loopback transport is unavailable.
2. **A real multi-pod Kueue victim.** A `Job`/`PyTorchJob` with more than one pod, the gate holding the
   arrival until every victim seals. Done when the harness prints `lost 0` for a multi-pod job, not the
   single `ckpttrainer` pod the S3 run uses.
3. **Shared-volume semantics.** Measure `rename`/`fsync` on NFS/Lustre, or state that the local-disk
   promise does not carry there. Done when [operator-notes.md](operator-notes.md) says which of the two
   it is, with a measurement or a clear refusal.
4. **Lightning and Composer examples.** The copyable loops are open issues #202 and #203. Done when
   `python3 examples/lightning_loop.py` and `composer_loop.py` print their one line.
5. **The remaining CLI conveniences.** `explain --json` (#105), `run --json` (#106), `world --evicted`
   (#107), and the executable README blocks (#108). Each is additive; none changes the promise.

## Not planned

macOS and Windows wheels, a Python re-implementation of the policy, platform deployments, certificates,
dashboards, a fleet-wide merged view, and a scheduler. The reasons are in
[0020-what-this-is-not.md](decisions/0020-what-this-is-not.md).

## How to read the rest

- What the release proved: the release pages and [CHANGELOG](../CHANGELOG.md).
- Why each decision: [decisions/README.md](decisions/README.md).
- What an experiment answers: [experiments-matrix.md](experiments-matrix.md).

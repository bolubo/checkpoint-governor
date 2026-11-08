# Roadmap

Status: 2026-11-05, current release `v0.29.0`. The direction is decision
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
- **A thirty-second first success.** `ckptctl demo` writes a real two-rank generation, breaks it, and
  prints the same resumable/verify answers a production copy gets; `docs/demo.md` carries the four
  faults.
- **One entry point.** `ckptctl setup` measures a volume (no cluster), checks a cluster read-only, and
  writes the four files a platform applies; `docs/install.md` §3.
- **An agent surface.** `ckptctl mcp` serves the read-only evidence bundle over stdio;
  `setup --agent=claude|codex` registers it and installs the skill; `docs/agents.md`.
- **One machine view.** `ckptctl evidence` and `--json` on explain/audit/verify/doctor share schema 1;
  `docs/evidence-schema.md`.

## Next, in order

1. **A real DDP/FSDP run.** Two ranks with gradient sync, the Governor on each, one engine, `resumable`
   for the job. Done when `torchrun --nproc_per_node=2` completes and `ckptctl resumable` answers on a
   host where its rendezvous works. **Gated on a two-GPU host and on usage**: 0.28 deliberately did not
   claim it.
2. **PyTorchJob/MPIJob victims.** The gate reads only `batch/v1 Job` today; a Kubeflow job is the next
   shape. Done when `setup --write` and the gate accept a PyTorchJob manifest.
3. **An object-storage provider.** The storage contract measures POSIX mounts; S3 has different rename
   semantics and needs its own adapter, not a promise borrowed from `local`.
4. **Lightning and Composer example loops.** The callbacks are supported and tested; the copyable loops
   are still open (#202, #203). Done when both print their one line under `make recipe-<name>`.
5. **Ray Train and Slurm launchers.** The four calls are the same; what changes is who starts the engine
   and where the rank comes from.

## Not planned

macOS and Windows wheels, a Python re-implementation of the policy, platform deployments, certificates,
dashboards, a fleet-wide merged view, and a scheduler. The reasons are in
[0020-what-this-is-not.md](decisions/0020-what-this-is-not.md).

## How to read the rest

- What the release proved: the release pages and [CHANGELOG](../CHANGELOG.md).
- Why each decision: [decisions/README.md](decisions/README.md).
- What an experiment answers: [experiments-matrix.md](experiments-matrix.md).

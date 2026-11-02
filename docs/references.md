# References — what the neighbours actually do

Status: research record, 2026-11-02. Companion: `docs/decisions/0027-the-platform-is-the-customer.md`,
`docs/plan-platform.md`.

The question this page answers is narrow: **in the world we are entering, who decides when a checkpoint
is written, and what does that decision rest on?** Every claim below was read from a repository through
the GitHub API on 2026-11-02; a `path:line` means the line was read, not inferred.

## How this was collected

- `gh search repos` found candidates, but its text search is noisy: a search for "checkpoint" returns
  Segment Anything. The rows are therefore candidates chosen by category and verified one by one.
- Star counts are popularity, not market size, and no download data is included. They say "this project
  is known", not "this project has users".
- The last column is an **inference**, not a measurement. It is the part that can be wrong.

## The table

| Project (★) | Who installs / who configures | What it promises | **Who decides when to write** | How it proves it |
|---|---|---|---|---|
| huggingface/transformers (168435) | pip / the job author | a training loop | **a step count, `save_steps=500`, by default** (`src/transformers/training_args.py:470-476`) — blind to time, deadline and pre-emption | nothing |
| Lightning-AI/pytorch-lightning (31438) | pip / the job author | a training loop | **one save per epoch by default** (`src/lightning/pytorch/callbacks/model_checkpoint.py`, docstring 159-163) | nothing |
| mosaicml/composer (5507) | pip / the job author | "training survives spot" | `save_interval='1ep'`, `num_checkpoints_to_keep=-1` (keep everything) (`composer/callbacks/checkpoint_saver.py:261,264`); `autoresume` defaults to `False` and needs `save_folder` + `run_name` (`composer/trainer/trainer.py:904,1078`) | a `latest-rank{rank}` symlink |
| NVIDIA/Megatron-LM (18312) | the launch script / the user | large-scale training | no default: `--save` requires an explicit `--save-interval` (`megatron/training/arguments.py:1211-1216`) | nothing |
| pytorch/pytorch (105076), DCP | user code | distributed save/load | **no policy at all**; `async_save` already exists (`torch/distributed/checkpoint/state_dict_saver.py:221`) | nothing |
| meta-pytorch/torchsnapshot (165) | user code | fast, memory-efficient snapshots | no policy | nothing |
| google/orbax (540) | JAX user code | checkpoint utilities | no policy | nothing |
| skypilot-org/skypilot (10781) | the platform / the user | "pre-empted jobs come back" | **says the job should checkpoint itself** (`docs/source/examples/managed-jobs.rst:214`); hardware and pre-emption are recovered by default, user-code failures are not (`:263`) | nothing |
| kubernetes-sigs/kueue (3128) | the operator, once | queueing, quota, pre-emption | not involved (no file with `checkpoint` in its name; not searched page by page) | nothing |
| volcano-sh/volcano (6052) | the operator, once | batch scheduling | not involved; the only related code is a restart state (`pkg/controllers/job/state/restarting.go`) | nothing |
| kubeflow/trainer (2271) | the operator, once | distributed training on Kubernetes | not involved | nothing |
| aws/aws-node-termination-handler (1763) | the operator, once | "handle instance shutdown gracefully" | not involved, but it **acts at the deadline** | events |
| osquery (23712) / falco (9546) / wazuh (17534) | the operator, once | host visibility, alerts | not involved | rules + events |
| restic (36941) / kopia (14556) / borg (13902) | the user | backup that can be restored | not involved, but **verification is explicit and expensive**: `restic check --read-data` and `--read-data-subset=n/t` (`doc/040_checking.rst:485-508`), `cli/command_snapshot_verify.go`, `src/borg/archiver/check_cmd.py` | read everything, or a sample |
| safetensors/safetensors (3940) | pulled in as a dependency | store tensors safely | not involved | — |

## What the table says

1. **The job author does not configure this.** Where a default exists it comes from the framework
   (500 steps, one epoch, keep everything); where it does not, the value is written into a launch script.
   The person we described in v1 as "never configured it" is real, and is already inside somebody
   else's default.
2. **Every existing default is blind in the same three ways**: it counts steps or epochs, not seconds;
   it does not know a deadline; it does not know what one write costs on this disk. That is not a
   quality problem we can fix inside their default; it is the reason a **policy** has to be a component
   rather than a bigger constant.
3. **Nobody owns the decision.** Frameworks expose knobs, libraries expose save/load, platforms restart
   jobs without asking whether a checkpoint exists. In the fifteen rows above, the answer to "who decides
   when to write" is never a component — it is a person, a constant, or nobody.
4. **"Born in" is proven only one way.** safetensors became a default by being a dependency that removed
   a risk (pickle) at zero cost (`transformers` lists `safetensors>=0.8.0` in `setup.py:128`, and its
   loader says "Use safetensors if possible", `src/transformers/modeling_utils.py:333`). No amount of
   features made that happen.
5. **Verification is sold as a costly, explicit act, not a default.** Every backup project ships a
   `check`/`verify` command whose documentation names its price in bandwidth and time. That is the shape
   our drill already has, and it is the shape a receipt should have.
6. **The default is an unrequested hedge.** A 500-step cadence is insurance against a *silent* death that
   the user never asked for and never priced. For an individual run whose only real events are the end of
   training and an announced stop, the correct write count is one. That is not a defect in the framework;
   it is a promise mismatch — and it is where a component that asks for the promise, instead of guessing
   it, earns its place.
7. **The installer, the payer and the beneficiary are different parties.** A cloud vendor bills the write
   and the longer job, so fewer writes can be *its* lost revenue; a cost-centre platform saves budget; a
   tenant saves time. Any direction that assumes one "customer" is wrong for at least two of the three.

## What this page does not prove

- It does not measure demand. Star counts are not adoption, and none of these projects publishes the
  number we would want: how often a job loses work and how much.
- "Nobody owns the decision" is a conclusion about **these fifteen projects**, not about the industry.
- The last column is inference. Two rows are explicitly marked as not searched page by page.
- It says nothing about whether our policy is *better*. That is the contrast's job
  (`docs/plan-library.md`, P0), and it can fail.

## What follows

`docs/decisions/0028-the-direction-is-a-library.md` draws the product conclusion — the installer, the
payer and the beneficiary are different parties, so the growth unit is an integration in the training
loop, not an install on a platform. `docs/plan-library.md` turns that into phases with a done-when for
each. `docs/plan-platform.md` is superseded and kept only as history.

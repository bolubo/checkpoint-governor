# Troubleshooting: start from the message

Every row starts with text a user can see. The fix is the second column, not a guess.

## Install and the first run

| you see | what it means | what to do |
|---|---|---|
| `error: externally-managed-environment` | the system Python is managed by PEP 668 and refuses `pip install` | `python3 -m venv .venv && . .venv/bin/activate`, then install again |
| `ckptctl: command not found` | the package is installed, but its venv is not active | activate the venv (`pip install` puts `ckptctl` beside `python`) |
| `no checkpoint-governor engine: the wheel should carry one` | the wheel has no engine inside, or `CKPTCTL_ENGINE` points nowhere | reinstall the wheel from PyPI (the engine is package data); for a source checkout, `make wheel` or set `CKPTCTL_ENGINE=/path/to/ckptctl` |
| `no state.json` / `should_save()` is always `False` | the engine never started, or died | read `.ckpt-governor/engine.log`; the client's fallback is to write at the end only |
| `should_save()` is always `False` and you expected a bound | no window is set, and nothing is at risk | set `engine_window="30m"` (the default is exceptions only) |
| `ValueError: ... you didn't pass an eval_dataset` | `eval_strategy` is `steps` but the Trainer has no evaluation set | add `eval_dataset=...`, or set `eval_strategy="no"` |

## Reading a checkpoint

| you see | what it means | what to do |
|---|---|---|
| `unknown: nothing sealed yet` | nothing has finished writing yet; this is not a bad copy | wait for the first sealed generation, or check the job's own save |
| `unknown: no declaration beside checkpoint-1000 (and no probe_cmd to ask)` | the copy exists, nobody said what it holds | write a declaration (the framework callback does it), or set `probe_cmd` in `.ckpt-governor/layout.json` |
| `not resumable: optimizer state absent` | the writer saved weights only (`save_only_model=True`, or a weights-only end write) | save the optimizer and scheduler with the same routine used during training |
| `not resumable: rank 1 shard missing` | the manifest names a rank whose file is not on this volume | find the missing shard, or copy the whole generation from the volume that holds it |
| `unknown: ... --generation is only for gen-* copies` | a framework checkpoint has no generation number | ask for the newest copy (`--dir` without `--generation`) |
| `best_model_checkpoint` is empty | no save landed on an evaluation boundary | use a window, or a `save_steps` multiple the run reaches; `docs/huggingface.md` §Three caveats |

## Status and commands

| you see | what it means | what to do |
|---|---|---|
| `flag provided but not defined: -dir` | `status` takes the directory as an argument | `ckptctl status /ckpt/job-1`, not `status --dir=...` |
| `phase writing`, `writing step 0`, minutes elapsed, after the job ended | the ledger's last round predates the job's final write (pre-0.26) | upgrade to 0.26; `status` now reads the directory's newest seal and shows `phase sealed` |
| `resume: resumable` but `last writes none` | the controller asked for nothing; the framework or the job wrote the copy itself | read `saving we wrote 0` beside it; the copy is still judged by `resumable` |
| `final write NOT made` from `ckptctl wrap` | the grace was shorter than the write | raise `--grace` (or the deployment's `grace_seconds`); the wrapper never pretends a write happened |

## When the message is not here

- The engine's own output: `.ckpt-governor/engine.log`.
- The files the job and the controller exchange: `docs/glue.md`.
- The platform's grace: `docs/operator-notes.md`.
- If the message is wrong rather than the setup, open an issue with the exact line and the command.

# First ten minutes

This page is one real run, on one machine, with the commands and the lines they print. Every command
below can be pasted. The job is a tiny model; nothing here needs a GPU.

## 1. Install, and see the command

```bash
python3 -m venv .venv
. .venv/bin/activate
pip install ckpt-policy
ckptctl version
```

```
ckptctl v0.27.0
```

The wheel carries the engine, so there is no second download. `ckptctl` is the same engine the Python
client starts; it is the command for `status` and `resumable`.

## 2. See the policy without training

```bash
python -m ckpt_policy.demo
```

```
policy                                      writes       io   overhead
framework default (save every 500 steps)         2    0.20s      0.00s
ckpt-policy, unconfigured                        1    0.10s      0.05s
```

The unconfigured arm writes once, at the end. That is the default: no periodic writes, and one write
when the job ends.

## 3. A real Trainer in ten lines of glue

Copy `contrib/ckpt-policy/examples/hf_trainer.py` from the repository, or run it from a checkout:

```bash
python3 contrib/ckpt-policy/examples/hf_trainer.py /tmp/ckpt-demo 200
```

```
trained 200 steps in 1.9s
checkpoints: 1
next: ckptctl resumable --dir=/tmp/ckpt-demo
```

During training the policy was silent; the one checkpoint is the Trainer's end write. The glue is the
marked block in the example: two imports, the Governor, `GovernorCallback`, four `TrainingArguments`
fields that move the alarm, `eval_dataset`, and `g.close()`.

## 4. Ask whether it can continue

```bash
ckptctl resumable --dir=/tmp/ckpt-demo
```

```
resumable (step 200, requires model, optimizer, scheduler, rng, caveat: the data position is not saved; a restore replays or restarts the epoch)
```

`unknown` is not “not resumable”: it means the copy exists but nobody said what it holds.
A `not resumable:` line names the state or shard that is missing.

## 5. See what the controller thinks

```bash
ckptctl status /tmp/ckpt-demo
```

```
unit ckpt-demo   generation 1   phase idle
newest sealed   step 200 at 2026-11-05T...
writing         none
window          off (exceptions only) · nothing time-based will write
resume: resumable (step 200, ...)
```

`newest sealed` here is the Trainer's own `checkpoint-200`; it is not a `gen-*` directory, because the
framework owns its layout. The `resume:` line is what answers for it.

`status` takes the directory as an argument, not `--dir`. The first line of the run is the one to read
when nothing is configured:

```
no-us none (unset — no writes if the governor stops) · window off (exceptions only)
```

It says exactly what will and will not be written.

## 6. Where the files are

```
/tmp/ckpt-demo/
  checkpoint-200/        the Trainer's own checkpoint, with resume.json beside it
  .ckpt-governor/
    state.json           the plan the client reads
    engine.log           the engine's own output; first place to look when state.json is absent
    ending               the job recorded its end
```

The job's checkpoint layout stays the framework's. `.ckpt-governor/` is the controller's own directory;
it is safe to delete after a run, and the next run recreates it.

## 7. What to try next

- A bound instead of silence: `Governor(job, start_engine=True, engine_window="30m")` asks for a write
  at most every 30 minutes of staleness.
- Several ranks: `docs/multi-rank.md`.
- A message that did not appear in this run: `docs/troubleshooting.md`.
- The platform's side of a stop: `docs/operator-notes.md`.

# Install for Python

Status: a wheel, `ckpt-policy 0.21.0`, linux/x86_64 — the same number as the release tag (decision
0029). The name is decided and publishing is trusted publishing from `.github/workflows/publish-pypi.yml`
(`RELEASE.md`); until the first upload lands, `pip install` means "install the wheel we built".

## 1. Build the wheel

```bash
make wheel
# engine: linux/amd64 (v0.21.0)
# ckptctl v0.21.0
# wheel: ckpt_policy-0.21.0-py3-none-linux_x86_64.whl, engine inside
```

The engine inside names the release, so a wheel that says `dev` is a wheel nobody built on purpose.

The wheel carries the Go engine as package data (decision 0028). It is built for **this machine**:
the tag names the platform, so a wheel built on linux/amd64 will not install on macOS by accident.

## 2. Install

```bash
python3 -m pip install dist/wheels/ckpt_policy-*.whl
```

No other install step: no `go install`, no PATH entry, no second download.

## 3. Use it

```python
from ckpt_policy import Governor, World

g = Governor("/ckpt/job-1", world=World(ranks=8), start_engine=True)
for step, batch in loader:
    g.step(step)
    if g.should_save():              # the plan asked; the whole write side, one line
        save(g.generation())         # your library writes the shards
        g.sealed(g.generation(), step, shards)
```

`start_engine=True` launches `ckptctl run` for that directory. If the engine exits, the plan stops
being refreshed and `should_save()` becomes false: the job still writes at the end and on an announced
stop, and nothing in the loop has to handle it.

## 4. See it work, without Go

```bash
python -m ckpt_policy.demo
```

```
policy                                      writes       io   overhead
framework default (save every 500 steps)         2    0.04s      0.00s
ckpt-policy, unconfigured                        1    0.02s      0.04s
```

The second column is the point; the third is what the client itself costs (one small file per step), and
it is printed rather than hidden.

## What you should see

- `.ckpt-governor/state.json` appears within a second, and `should_save()` is `False` while nothing is
  at risk. That is the product: silence, with a plan behind it.
- `.ckpt-governor/engine.log` holds the engine's own output. It is the first place to look when the
  plan does not appear.
- `g.close()` (or the `with` form) stops the engine. `close()` is safe to call twice.

## When it does not work

| symptom | cause | what to do |
|---|---|---|
| `no checkpoint-governor engine` | the wheel has no engine inside | rebuild with `make wheel`; or set `CKPTCTL_ENGINE` to a `ckptctl` binary |
| no `state.json` | the engine died or never started | read `engine.log`; `should_save()` stays false, which is the honest fallback |
| `should_save()` never true | nothing is at risk, and no window is set | that is the default; set `engine_window="30m"` if you want a bound |

## What this does not do

It does not re-implement the policy in Python. The decision stays in the Go engine; the client starts
it, reads the plan and reports. A second implementation would be a second policy (0028).

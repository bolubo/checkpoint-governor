# See it in thirty seconds

```bash
pip install ckpt-policy
ckptctl demo
```

```
demo /tmp/ckpt-demo: 2 ranks, gen-0001, step 300, storage local
fault: rank1.pt deleted — the kill landed between the shards and the manifest
status      newest sealed step 300 · writing none
resumable   not resumable: rank 1 shard missing
verify      broken: shard rank1.pt is missing
evidence    1 problem · storage local (process death)
next        ckptctl demo --repair --dir=/tmp/ckpt-demo
```

```bash
ckptctl demo --repair
```

```
demo /tmp/ckpt-demo: 2 ranks, gen-0001, step 300, storage local
fault: none — the shard was rewritten and the generation sealed again
status      newest sealed step 300 · writing none
resumable   resumable (gen 1, step 300, world 2/2, full state)
verify      verified: content matches the manifest
evidence    0 problems · storage local (process death)
next        ckptctl resumable --dir=/tmp/ckpt-demo
```

The demo writes a real generation — two shards, a manifest, a declaration — and then breaks it the way
a killed job breaks one. Nothing is simulated in prose: `resumable` and `verify` are the same commands
that judge a production copy.

## The four faults

| `--scenario` | What is broken | What answers |
|---|---|---|
| `kill` (default) | `rank1.pt` deleted | `not resumable: rank 1 shard missing` |
| `torn` | the last 4 bytes of `rank1.pt` changed, size intact | `verify` says the tail hashes differently |
| `short` | `rank1.pt` truncated | `verify` says the size differs |
| `clean` | nothing | `resumable` and `verified` |

`torn` and `short` are the interesting pair: the shard is still **present**, so `resumable` says yes and
`verify` says no. A copy can be declarable and not whole at the same time, and both answers are true.

## Flags

```bash
ckptctl demo --scenario=torn        # a different fault
ckptctl demo --repair --dir=<dir>   # rewrite the shard and seal again
ckptctl demo --clean --dir=<dir>    # remove the demo directory
ckptctl demo --json                 # the same report as one object
```

- The default directory is `/tmp/ckpt-demo`, and it is left in place so the next command can point at
  it (`ckptctl status /tmp/ckpt-demo`, `ckptctl resumable --dir=/tmp/ckpt-demo`).
- The demo refuses a non-empty directory it did not create. `--clean` refuses without the `.ckpt-demo`
  marker, so `ckptctl demo --clean --dir=/ckpt/prod` cannot delete a real checkpoint.
- `--json` is the same report a script or an agent sees; its `storage.class` comes from a real probe of
  the directory (`docs/storage.md`), not from a guess.

## What it does not claim

- The shards are synthetic: this is a tutorial, not a training run. The shapes, the manifest and the
  verdicts are the real ones.
- It does not start the engine or write a ledger, so `ckptctl status` answers `not governed yet` until
  a real run writes one.
- It does not test a network volume; `doctor --storage` does that on the mount you give it.

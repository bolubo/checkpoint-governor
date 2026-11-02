# schema-2 fixture

`job-1/` is a checkpoint directory written by the binary on **2026-11-02**, and it is checked in on
purpose: it is the only evidence that a plan written by that build still reads the same in a later
one.

It was produced by the real binaries, not by hand:

```bash
ckpttrainer --dir=job-1 --steps=4 --step-ms=10 --write-ms=10 --shard-bytes=256 --ranks=1
ckptctl run --dir=job-1 --unit=job-1 --once --no-us=none
```

The unit id was passed as `job-1` (not the temporary directory) so the fixture carries no path from
the machine that produced it. Nothing else was edited.

What it pins:

- `.ckpt-governor/state.json` — schema 2, epoch 1, `action: skip`, `reason: calm`, and the plan's
  sentences. A change to the reader, the schema or the vocabulary fails here first.
- `.ckpt-governor/internal.json`, `attempts.json` — the ledger and the attempt record as that build
  wrote them.
- `step-3/` — a sealed copy: manifest plus one rank's bytes.

Regenerate only with a decision record that says why the previous fixture stopped being valid; a fixture
edited by hand proves that the parser agrees with the editor, which is not the property we want.

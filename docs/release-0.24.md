# v0.24.0 — the copy can be judged, whoever wrote it

This release removes the assumption the product had carried since its first verdict: that the newest
checkpoint is a `gen-N/` directory this build wrote. A job that saved through its framework had copies
on disk and the product could not see them.

## What the long runs did not catch

Real cluster runs had exercised the write path for hours: the write happened, the bytes verified, the
retention kept the right copies. Each test asked its own question, and none asked "can the newest copy
be judged at all?" — so a `checkpoint-1000/` left by the Trainer was silently outside every answer.

The receipt made it visible only after the fix: a directory with copies and no declaration now prints

```
resume: unknown: no declaration beside checkpoint-1000 (and no probe_cmd to ask)
```

Silence was the old answer, and silence reads like "fine".

## What it does now

**One finder for every layout.** `resumable`, `status`, the receipt and the loop all ask
`internal/layout`. Preference is fixed: `gen-*` first, then the highest step; at the same step
`checkpoint-*` > `*.ckpt` > a Composer episode. A directory with no completion mark and no declaration
is a write in flight, reported as `unknown: … is not sealed`, never counted as the newest copy.

| layout | recognised by | sealed when |
|---|---|---|
| `gen-N/`, `step-N/` | directory + `manifest.json` | the manifest exists |
| `checkpoint-N/` (Hugging Face) | directory name | `trainer_state.json` exists, or a declaration does |
| `*.ckpt` (Lightning) | suffix | the file is non-zero |
| a Composer episode | `<file>.resume.json` beside it | the declaration and the artifact exist |
| your own layout | `layout.json`'s `glob` | a declaration, or a successful probe |

**Declarations gained five optional fields.** `requires` (the ruler this job is judged against — five
states by default, four for a framework), `target` (`"."` or a sibling file name), `caveats`
(`data-position-not-saved`), `min_reader` (a newer file is refused with the upgrade named), and `ext`
(the only extension point). A declaration written before these existed keeps the sentence it had.

**Every adapter declares.** Hugging Face writes `checkpoint-<step>/resume.json`, Lightning writes
`<file>.ckpt.resume.json`, Composer's rank 0 writes `<artifact>.resume.json`. `ckptctl declare` does
the same by hand for copies that predate the integration, and `layout.json`'s `probe_cmd` is the last
glue for a format we cannot read (five seconds, then `unknown: probe_cmd failed`, never "not resumable").

**The stop protocol has an executor.** A framework callback cannot wait for `may-exit`, so on a real
host the handshake had no one to run it. `ckptctl wrap` takes the signal, writes `stopping`, waits until
a sealed copy lands above the step it saw (or the deadline), then forwards:

```
$ ckptctl wrap --dir=/ckpt/job-1 --grace=60s -- python train.py
wrap: signal received, stopping written, waiting up to 1m0s
wrap: sealed at step 1000 (18.4s) → forwarding SIGTERM
```

Proven end to end with real processes at grace 60/30/10, three runs each: 60 and 30 seal and lose
nothing; 10 prints `final write NOT made` (a 12 s write cannot land in a 10 s grace). A second signal
forwards at once and records `release-forced`.

**Multi-rank ownership.** Each rank writes `ranks/<rank>/step`; only rank 0 writes a team-level file —
`lead-only file` is an error, not a silent overwrite. Progress is the slowest reporting rank, `status`
prints `members 2/2 · spread 4` and names silent ranks. A declared multi-rank copy whose manifest is
missing a rank's shard is `not resumable: rank N shard missing`.

**Retention and counting stay honest.** Retention removes our own layouts only; a framework's
checkpoint is the framework's to remove. The receipt's "on disk" number and `status` come from the same
finder, so they cannot disagree.

## What is tested

```bash
$ make test-claims
claims: 30 · tests found: 30 · missing: 0
```

Thirty outbound promises, each naming the test that would fail if it stopped being true. The failure
injections the release adds: a half-written directory, a copy with no declaration, a declaration from a
newer build, and a probe that never answers.

`./hack/prepush.sh` runs the ten-step CI gate; `hack/wrap-grace.sh` and `hack/members.sh` are the two
real-process proofs quoted above.

## What is not promised

- **No built-in format sniffing.** We do not read pickle or zip internals; `probe_cmd` is how an
  opaque copy becomes known.
- **No cross-node coordination in `wrap`.** One wrapper per node waits for its own copy; the stop is
  coordinated per process group, not across the gang.
- **No rank barrier.** All ranks reaching the same step is the framework's business; this release
  reports a spread and never suppresses a write because of it.
- **Real Lightning and Composer runs are v0.25.** Their contracts and declarations are tested in CI
  without the frameworks installed; the real-run targets stay in `docs/plan-library.md`.
- **macOS and Windows wheels remain v1.**

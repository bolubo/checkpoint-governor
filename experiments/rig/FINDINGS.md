# What the real runs found

A log of things the experiments taught us, in the order they appeared. Each entry says what happened, what it
means, and whether it changed the product. Entries stay here even after they are fixed: a finding that
disappeared without a trace is a finding somebody will have again.

## F1 — the writer did not speak the contract

**Seen:** building the matrix for 4.2. `ckpttrainer` wrote `step-*` directories whose manifest has a
different schema and no `tailSha256`.

**Means:** the classifier could only ever answer `invalid`. A seven-row fault matrix would have been the same
row seven times, and — worse — it would have looked green.

**Changed:** `--contract` writes `gen-N` with a manifest sealed last and a declared tail hash. The rule this
leaves behind: **before measuring a fault, check that the writer can be judged at all.** A matrix against a
non-conforming writer measures the harness, not the product.

## F2 — two writers can leave a directory that verifies, with someone else's debris in it

**Seen:** `dual-writer`, two trainers racing for one generation with different shard counts and sizes
(2×64 KiB and 3×96 KiB). Two runs, two answers:

```
run 1:  generation 1  verified 12:46:45  sample  steps 1
run 2:  generation 1  sealed 04:49:24  steps 1  short (shard rank-00.bin is 98304 bytes, sealed at 65536)
```

**The case is non-deterministic by nature**, and that is the finding rather than a nuisance: whichever writer
seals last owns the manifest, and whether the other writer's bytes happen to contradict it is a matter of
timing. The classifier never verified a *torn* set — the second run's `short` is it doing its job — but a
`verified` verdict can still be true of a directory that another process is writing into.

**Means:** the winning write sealed a manifest that described its own shards, and at verification time those
shards matched. But the other writer's files — the ones the winning manifest does not list — are still in the
directory, and **the classifier cannot see them**: the manifest is the only index it has.

That is a real limit, not a bug in the code as written. Two consequences:

1. a verified generation may sit in a directory that another process is still writing into, so "verified" is a
   statement about a moment, not about the directory;
2. an operator cleaning up cannot tell which files belong to the generation and which are a dead writer's
   leftovers.

**Not yet changed.** The candidate rule is to list the directory and treat any file the manifest does not
name as `unsealed` (or at least as a warning in the receipt). It is deliberately not implemented in the same
breath as the finding: the rule needs to say what to do about `.ckpt/` and the request file, which are
legitimately unlisted.

**Also worth noting:** the contract's "one write per volume at a time" is currently advice. Nothing enforces
it, and the experiment that ignores it does not look wrong from the outside — which is exactly why it needs a
finding rather than a paragraph.

## F3 — the receipt does not carry the seal time

**Seen:** the matrix CSV's lines read `generation 1  verified 12:46:44  sample  steps 1`. There is no
`sealed 12:46:41` between them, although the manifest has `sealedAt`.

**Means:** `Summarize` builds receipts from `Classify`, which returns the verdict but not the manifest's
seal time, so the middle of the line is empty. The three claims are `requested`, `sealed`, `verified` — and
one of the three is currently missing from the walk output.

**Changed:** not yet. It is a one-field fix, and it belongs with the `requested` side (which needs the
request file's mtime), so doing it properly is a small change to `Classify` rather than a patch to the
printer.

## F4 — the matrix's detail column is empty for the rows that pass

**Seen:** `clean,generation 1  verified …,,` — the detail field is blank on success.

**Means:** `ckptctl verify --all` prints a detail line only when there is something to say, so the script's
`sed -n '2p'` picks up a blank line for a passing row. Harmless for a human, and a trap for anything that
parses the CSV by column.

**Not changed.** A CSV whose columns shift by row is the kind of thing that gets trusted for a month and then
breeds a wrong chart. When the matrix becomes part of `make ci` (Day 3), the row should be built by a
structured emitter rather than by `sed`.

## F6 — the receipt mixed two clocks and invented eight hours

**Seen:** after F3 was fixed, one line read:

```
generation 1  sealed 04:48:49  verified 12:48:49  sample  steps 1
```

Eight hours between two events that happened in the same second.

**Means:** the seal time comes from the writer's manifest (UTC, as everything in the contract is), and the
verify time comes from our clock formatted in local time. Neither was wrong on its own; printed side by side
they made a timeline that never happened.

**Changed:** the receipt formats all three timestamps in UTC. A log line that means different things on
different machines is worse than one that is inconveniently offset.

## F5 — a rebuild from a stale tree silently reverted a rule-fix on main

**Seen:** rebuilding #145 on current main by copying whole files from an older worktree put back
`docs/product-v1.md` as a citation in `run.go`, which `TestCommentsCiteDecisionsNotPagePaths` catches.

**Means:** the build was fine, the tests were fine locally — because the local tree did not have that test.
Copying files between trees is a merge, and it was done as a copy.

**Changed:** the rebuild restored the file from main and re-applied the single intended edit. The rule:
**when transplanting work onto a newer main, take main's file and re-apply the change; never overwrite the
file.**

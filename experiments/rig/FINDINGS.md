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

**Changed — reported, not judged.** `Classify` now lists what the generation directory holds that the
manifest does not name, and the verdict carries it:

```
$ ckptctl verify --dir=/ckpt/gen-0007
verified ok
  content matches the manifest
  steps 48210
  1 unlisted: rank-99.bin
```

**Why a warning rather than a failure.** A writer may legitimately keep sidecars next to its shards, and a
verifier that fails on an unlisted file would reject correct work. What it must not do is stay silent: an
unlisted file is the only visible trace of a second writer, and `verified` is otherwise a statement about a
manifest rather than about a directory.

**What is still not solved.** Nothing enforces "one write per volume". The verdict now shows the debris but
does not prevent it — that is a scheduling constraint (Kueue has no such primitive either), and the honest
answer is a contract requirement plus this warning, not a lock invented here.

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

**Changed:** the CSV is `fault,line` — one row, one line, commas neutralised — so a column cannot shift by
row. The fragile second `sed` call is gone rather than guarded: a field that exists only on failure is a
field that will be misread as success one day.

**Still owed:** when the matrix becomes part of `make ci` (Day 3), the row should come from a structured
emitter rather than from formatted text at all.

## F5 — a rebuild from a stale tree silently reverted a rule-fix on main

**Seen:** rebuilding #145 on current main by copying whole files from an older worktree put back
`docs/product-v1.md` as a citation in `run.go`, which `TestCommentsCiteDecisionsNotPagePaths` catches.

**Means:** the build was fine, the tests were fine locally — because the local tree did not have that test.
Copying files between trees is a merge, and it was done as a copy.

**Changed:** the rebuild restored the file from main and re-applied the single intended edit. The rule:
**when transplanting work onto a newer main, take main's file and re-apply the change; never overwrite the
file.**

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

## F7 — retracted: I wrote a finding on top of an experiment that never ran

**What I first wrote:** that an unmounted volume leaves the writer quietly writing to the container's root
filesystem, producing a checkpoint we verify.

**What actually happened:**

```
$ umount /tmp/u1
umount: /tmp/u1: target is busy.
```

The kernel refuses to unmount a filesystem with open files. My script ran `umount ... 2>/dev/null || true`,
so the failure was invisible, the mount stayed up, and the `verified` I saw was **correct behaviour on a
volume that had never gone away**.

**Why this is written down instead of deleted:** the wrong version was plausible, specific, and had a
mechanism — the kind of finding that gets repeated in a design review six months later. A retraction with the
command output is worth more than a deleted paragraph.

**Changed:** the scenario now records whether the unmount actually happened. A refused unmount is its own
result, not a data-loss story.

## F8 — the instrument lied twice, and both times plausibly

Two entries in this file were produced by a broken measuring device rather than by the system under test.
They are together because the lesson is the same one, twice.

**(a) A suppressed precondition.** `umount "$mnt" 2>/dev/null || true`. The kernel refuses to unmount a
filesystem with open files; the `|| true` hid it, the mount stayed up, and the run "proved" a scenario that
had not happened. The script now records which detach actually occurred — `unmounted`, `lazy-detach`, or
`none` — so a refused unmount is a result instead of a silence.

**(b) A lost exit code.** `wait $pid; echo "rc=$?"` reported 0 for a process that was failing, because `$?`
does not survive this shell invocation. Measured directly:

```
$ sh -c 'exit 7'; echo rc=$?
rc=0
$ if sh -c 'exit 7'; then echo ok; else echo failed; fi
failed
```

The status is there; the *expansion* is not. Every exit-code claim made with `$?` through a wrapped shell is
therefore suspect, and the ones still standing here were re-measured with control flow
(`cmd || echo FAILED`).

**Rule:** a suppressed error is a suppressed result, and a status read through an interpreter you have not
tested is a guess. Before trusting a harness, make it fail on purpose and check that the failure arrives.

## F9 — retracted: the writer fails loudly, as it turned out

**What I first wrote:** a failed seal is printed and the process exits 0, so any supervisor sees success.

**What actually happened:** the exit code was lost by the shell (F8b). Measured with control flow:

```
$ /tmp/trainer2 --dir=/proc/denied --steps=1 >/dev/null 2>&1 || echo FAILED
FAILED
```

and for a save that fails mid-run, after the directory is deleted under it: `FAILED` as well. The writer
exits non-zero in both cases; `main` prints the error and calls `os.Exit(1)`, exactly as the code reads.

**Kept, not deleted:** two findings in one session came from instruments rather than from the system, and
both were more specific and more alarming than the truth. That ratio is the argument for keeping this file.

## F10 — the cluster runs, and the loop is not in it

**Seen:** `kueue-lab` started, the controller came up 1/1, and `experiments/kueue/run.sh c1` ran a case to
completion. All five holders admitted, and the arrival **also admitted** — the case that exists to prove
preemption did not preempt.

**Means two things, and only one of them is about Kueue.**

1. That case no longer exercises what its name says. Either the priority shape changed under it or the lab's
   quota now fits one more unit; a case that quietly stops testing its own claim is worse than a failing one,
   because it stays green.
2. **The loop is not in the cluster at all.** There is no `make deploy` here, no governor workload, no
   `ckpttrainer` running under Kueue. The matrix proves the *classifier*; `run.sh` proves *preemption*; the
   join — a request that reaches a writer, a write that verifies, under a real eviction — has never run.

**Not claimed:** that the cluster proves the loop. It does not, and the Day 4 write-up must say so in those
words rather than implying it from two green halves.

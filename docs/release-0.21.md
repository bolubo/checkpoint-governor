# v0.21.0 — the loop closes, and the receipt tells the truth

This release is the first where the whole chain has been run end to end under a real eviction, and the first
where the numbers a customer would be shown have been checked against a real volume.

## What the receipt says now, and what it said before

**If you write checkpoints under the generation layout (`gen-N`), the previous receipt could say `no writes`
while your checkpoint directory was filling up.** The product counted only `step-` directories, so a writer of
the other layout was invisible to its own accounting.

That is the one sentence this product must never get wrong: a customer reading "nothing was written" may well
go and delete the directory themselves. Fixed in this release, and pinned by two tests — a sealed generation
counts, an unsealed one does not, and `.ckpt-governor` never does.

## The loop, under a real eviction

```
holders admitted: 4 (of 4)
taken after 3s: job-writer-c3df0     False
generations 18 · verified 18 · broken 0 · unchecked 0
invalid rate 0.00%
```

A writer takes quota under the contract, an intruder above it makes Kueue take the writer, and the volume is
judged afterwards by the same binary a customer runs. `experiments/cluster/join/run.sh` reproduces it and
deletes everything it created, including on failure.

## Disk, both sides

- **The sweep is bounded in bytes**, not wall time, and runs every round — a job that is preempted, retires a
  copy, and then stops writing no longer leaves that copy in `.trash` forever.
- **A round's sweep must be able to give back what that round retired.** A fixed budget smaller than one copy
  per round loses ground every round; measured at 31 copies before the rule existed.
- **Retention tightens under space pressure**: under two copies of headroom, keep one rather than three. A
  rewrite that cannot land is worse than a smaller retention.
- **The ledger forgets old paths**: the one unbounded map is capped at its newest 256 entries.
- A full volume is now a test row: three copies survive and verify, the fourth fails with ENOSPC, and the writer
  exits non-zero.

## For whoever builds this repository

`hack/pr-check.sh <file>...` builds **the tree the PR will be**, not the working directory. Three pull requests
went red today because CI compiles *main plus the PR's file list* — a smaller tree than the one on disk — and a
declaration or a test was missing from that list. The script now also runs `go vet`, `go test`, and checks that
the Makefile still has its `fast` target.

`hack/footprint.sh` reports what this machine has spent on experiments — docker volumes and images, kind
clusters, caches — and exits non-zero over budget. It reports; it does not delete, because a tool that deletes
other people's images gets run once.

## A note on the version numbers

The repository carries `v0.15.0` (04:13 today) and `v0.2.0` (05:56 today, which took over "Latest"). Two
numbering schemes crossed. **v0.21.0 is the newest by date and supersedes both**; the next release should settle
on one scheme rather than add a third.

# Changelog

One entry per released tag. The prototype's history before this file is `git log`; this file starts at
the first release that makes a compatibility promise.

## v0.21.0 — 2026-11-02

The first release where the whole chain was run under a real eviction, and the first where the receipt's numbers
were checked against a real volume.

**The receipt now counts what a generation-layout writer (`gen-N`) produces.** Before this release the product
counted only `step-` directories, so a writer of the other layout was invisible to its own accounting: the receipt
could say `no writes` while the checkpoint directory filled up. A customer reading that sentence may well go and
delete the directory themselves — which is why this is the first thing in these notes. Two tests now pin it.

**Under a real preemption** (`experiments/cluster/join/run.sh`, reproducible and self-cleaning):

```
holders admitted: 4 (of 4)
taken after 3s: job-writer-c3df0     False
generations 18 · verified 18 · broken 0 · unchecked 0
invalid rate 0.00%
```

**Disk.** The sweep is bounded in bytes and runs every round, so a job that is preempted and then stops writing
no longer leaves a copy in `.trash` forever; a round's sweep must be able to give back what that round retired
(without it the trash grew at 31 copies); retention tightens under space pressure; the ledger's one unbounded map
is capped. A full volume is now a test: three copies survive and verify, the fourth fails, the writer exits
non-zero.

**Building this repository.** `hack/pr-check.sh` builds the tree a PR will be, not the working directory — CI
compiles *main plus the PR's files*, and four pull requests went red today because a declaration or a test was
missing from that list. `hack/footprint.sh` reports what a machine has spent on experiments and fails over
budget; it reports rather than deletes.

## v0.15.0 — 2026-11-02

The release a stranger can install, and whose promises are tested. The decision core does not change;
what is new is the surface around it. Scope and acceptance lines: `docs/product-v0.15.md`.

**Install.** `curl -fsSL …/install.sh | sh` downloads the tarball for your architecture, checks it
against `SHA256SUMS`, and only then puts `ckptctl` on your PATH. `make install-smoke` proves that path
offline against a locally built `dist/`, and CI runs it on every push.

**Compatibility.** The plan schema is a range: `doctor` prints which schema wrote a plan and refuses
one this build cannot read, with exit code 2. A directory written by the previous build is checked in
as a fixture, and a run on it continues its ledger instead of starting over. The names a scraper or a
script keys on — six metrics, nine event kinds, fourteen conditions, the exit codes, the flags — are
pinned by `TestFrozenNames`, and `docs/compatibility.md` says what is not promised.

**Proof.** `make spot` preempts a real job in ten seconds: one write, one kept checkpoint, the cause
and the cost printed as a receipt.

**Docs.** `docs/install.md` names the three shapes and the files each side writes; `deploy/` gains
copyable `conditions.json` and `deployment.json` beside the manifest.

`make ci` and `hack/prepush.sh` run the same gate: `check`, `e2e`, `exp`, `demo`, `install-smoke`,
`spot`.

Not in this release: the training-script callback and the resume contract. They are v0.2, and are
named there so they do not leak into this one.

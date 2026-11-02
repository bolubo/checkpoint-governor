# Changelog

One entry per released tag. The prototype's history before this file is `git log`; this file starts at
the first release that makes a compatibility promise.

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

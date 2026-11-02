# Changelog

One entry per released tag. The prototype's history before this file is `git log`; this file starts at
the first release that makes a compatibility promise.

## v0.15 — planned

Not released. Scope and acceptance lines: `docs/product-v0.15.md`.

- [x] one-line install: a static binary plus `SHA256SUMS`, and a `curl | sh` path that
  verifies the checksum before installing (`install.sh`, `make install-smoke`)
- [x] plan schema in `doctor` (which version wrote it, which this build reads), a checked-in
  previous-build fixture, and `docs/compatibility.md`
- [x] the frozen lists (conditions, event kinds, metrics, exit codes, flags) pinned by
  `TestFrozenNames`
- [x] a run on a directory from the previous build continues its ledger instead of starting over
- [x] spot simulator in the rig, ending in a receipt (`make spot`)
- [x] `make release-notes` and a tag workflow that publishes the checksum-verified tarballs
- [x] `docs/install.md` and a self-contained `deploy/` (manifest, example configs)

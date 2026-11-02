# Changelog

One entry per released tag. The prototype's history before this file is `git log`; this file starts at
the first release that makes a compatibility promise.

## v0.2 — planned

Not released. Scope and acceptance lines: `docs/product-v0.2.md`.

- [ ] one-line install: a static binary plus `SHA256SUMS` (`make dist`), and a `curl | sh` path
- [ ] `contrib/ckptctl-pytorch/`: the callback a training script adds in one line
- [ ] plan schema number, `doctor` reading range, and a checked-in previous-version fixture
- [ ] one frozen-names page (metric names, event codes, exit codes, flags, conditions) pinned by a test
- [ ] upgrade-across-a-crash test: old process writes, killed, new process takes over
- [ ] spot simulator in the rig, ending in a receipt
- [ ] `docs/install.md` (binary / container / callback) and a self-contained `deploy/`

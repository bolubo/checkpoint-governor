# 0029 — One version number

Date: 2026-11-02. Status: accepted. Companion to 0028 (the delivery shape) and `RELEASE.md`.

## Context

The repository carries four tags, and two numbering schemes crossed in a single day: `v0.1.0` (13:54),
`v0.15.0` (04:13, a product phase), `v0.2.0` (06:01, another product phase that took over "Latest"),
and `v0.21.0` (09:25, a release line). Pages are titled v0.2 and v0.15, the Python package says `0.2.0`,
and `docs/release-0.21.md` ends with "the next release should settle on one scheme rather than add a
third".

What that costs is not cosmetic. `gh release list` puts **v0.21.0 as Latest** while the two most
quotable pages are titled after lower numbers, so a stranger cannot tell which number to install.
Worse, the wheel's engine cannot say which release it is: `hack/build-wheel.sh` builds without
`-X main.version`, so `ckptctl version` inside the package prints `dev`.

## Decision

**One number. `vX.Y.Z` on the code and the tag; the Python package carries the same number without the
`v`.**

1. **`v0.21.0` was the current number when this record was written.** Later releases follow the same rule
   without a new record; `v0.24.0` is the current number at the time of 0032. The two earlier numbers are closed: they stay published, they
   are never renumbered, and the pages that describe them are history rather than install instructions.
2. **A tag publishes both artifacts.** `.github/workflows/publish-pypi.yml` runs on `v*`, from the same
   tree the binaries and the image come from, and `workflow_dispatch` exists only for the first upload
   (that tag predates the workflow).
3. **The engine reports the number.** `hack/build-wheel.sh` reads `version` from `pyproject.toml` and
   passes `-X main.version=vX.Y.Z`, so `ckptctl version` inside the wheel names the release it is.
4. **A version on PyPI is immutable.** A wrong wheel is fixed by the next number, never by replacing a
   file.

## Evidence

### FACT

```
$ gh release list --limit 4
v0.21.0	Latest	v0.21.0
v0.2.0		v0.2.0
v0.15.0 — the contract, the install line, and the proof	v0.15.0
v0.1.0 — checkpointing, decided for you	v0.1.0
```

```
$ python3 -c "<read METADATA of dist/wheels/ckpt_policy-*.whl>"
Version: 0.2.0
$ contrib/ckpt-policy/ckpt_policy/_bin/ckptctl version
ckptctl dev
```

### ASSUMPTION

None. The two artifacts are built from one tree by one command, so their agreement is checked by the
artifact rather than promised by a page: the wheel's `METADATA` and the engine's `version` output are
both printed by `make wheel`.

## Consequences

- `contrib/ckpt-policy/pyproject.toml`, `ckpt_policy/__init__.py` and `CITATION.cff` carry `0.21.0`.
- `hack/build-wheel.sh` takes the version from `pyproject.toml`, so the wheel cannot disagree with it.
- `docs/release-0.21.md`, `docs/compatibility.md`, `docs/index.md` and the README say which number is
  current, once each.
- The PyPI precondition in `docs/plan-library.md` is answered: the name is `ckpt-policy`, publishing is
  trusted publishing.

**Deleted:** the idea that a second numbering scheme can start "just for the package". The next phase
gets a number in this line, not a new one.

## What this does not promise

No source distribution, and no platform other than `manylinux_2_17_x86_64` — the wheel contains a
binary, and the classifier says so. Publishing the same release to PyPI is not a promise of "same hour
as the tag"; the rule is that the two artifacts are built from the same tree, not that they land
together.

# Releasing

One tag produces everything a user downloads. The tag workflow is `.github/workflows/release.yml`;
this page is the part a person has to decide.

## The steps

```bash
git tag -a v0.6.0 -m "v0.6.0"
git push origin v0.6.0
```

The workflow then:

1. builds `dist/checkpoint-governor_v0.6.0_linux_{amd64,arm64}.tar.gz` and `dist/SHA256SUMS`
   (`make dist VERSION="${GITHUB_REF_NAME}"`);
2. builds and pushes `ghcr.io/bolubo/checkpoint-governor:v0.6.0` for `linux/amd64` and `linux/arm64`;
3. creates the GitHub release with the archives, the checksums and `--generate-notes`.

The same two commands run locally, and `make dist VERSION=v0.6.0` is the half that needs no Docker:

```
dist: 2 archives, 2 checksums
```

## What a version has to satisfy

- The binary reports the tag: `ckptctl version` prints `ckptctl v0.6.0` (set with `-ldflags
  "-X main.version=..."`, and the image passes it as a build argument).
- `verify` is green on the commit the tag points at. The tag is not a way to skip the gate.
- The checksums cover the archives, not the binaries inside them: one line per download.

## Version sequence

The numbered line continues from the project this repository replaced (`v0.5.0` is the first
release of this codebase). A release that changes a decision record is a minor version; a release
that only fixes a bug inside an existing decision is a patch.

## The Python package

`ckpt-policy` is published by `.github/workflows/publish-pypi.yml` from the same tag as the binaries, and
the wheel carries the Go engine, so `pip install` needs nothing else. It carries the release number
without the `v` (decision 0029), which is why the two artifacts are published from one tag rather than
from two.

PyPI trusts this repository once, and that configuration is the only step a person does:

```
PyPI -> Publishing -> add a pending publisher
project name  ckpt-policy
owner         bolubo
repository    checkpoint-governor
workflow      publish-pypi.yml
environment   pypi
```

After that, publishing is the tag itself, and the manual form exists for the first upload only
(`v0.21.0` was tagged before this workflow did):

```bash
sh hack/release.sh v0.22.0          # the tag also triggers the wheel
gh workflow run publish-pypi.yml    # manual, for a version whose tag already exists
```

The workflow builds the wheel with `hack/build-wheel.sh` — the same script a laptop runs — and refuses
to publish a wheel without the engine inside. It publishes **one platform**, `manylinux_2_17_x86_64`:
the binary is the engine, and the raw `linux_x86_64` tag is refused by PyPI itself
(`400 ... has an unsupported platform tag`). The classifier says Linux rather than leaving pip to
discover it.

**A version on PyPI is immutable.** A bad wheel is fixed by publishing the next version; the old file
stays, and is never replaced or deleted.

**The binary and the wheel are one version, not two.** They read the same declaration schema, so a
release that adds an optional field ships both halves or neither. A declaration may name `min_reader`;
a reader below it refuses with the upgrade named instead of misreading the file. That is why
`publish-pypi.yml` and `release.yml` run from the same tag and why the wheel carries the engine.

## Rollback

A bad tag is never moved. Delete the release, then delete the tag and re-tag the previous commit —
but a pulled image keeps its digest, so a user who needs the old binary should use a digest, not a
tag. `docker pull ghcr.io/bolubo/checkpoint-governor@sha256:...` is the only rollback that is exact.

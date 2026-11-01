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

## Rollback

A bad tag is never moved. Delete the release, then delete the tag and re-tag the previous commit —
but a pulled image keeps its digest, so a user who needs the old binary should use a digest, not a
tag. `docker pull ghcr.io/bolubo/checkpoint-governor@sha256:...` is the only rollback that is exact.

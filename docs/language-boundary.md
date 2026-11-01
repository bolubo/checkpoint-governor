# ADR: which language owns which part

Date: 2026-11-01. Status: accepted. Companion to `CONTRIBUTING.md` §Language, which fixes the natural
language; this fixes the programming ones.

## Context

This repository is written in four languages, and until now each was chosen by whoever was holding
the keyboard:

- **Go** — `cmd/` and `internal/`, the product.
- **Python** — exactly one file, `experiments/rig/verify.py`, which decides whether a checkpoint
  survived.
- **POSIX shell** — `experiments/rig/*.sh` and `experiments/demo/demo.sh`.
- **Markdown** — `docs/`, README, the decision records.

Mixing them without a rule is how a project acquires a second, half-finished implementation of its
own product in a scripting language. The rule below is not about taste; it is about what each part has
to be able to do.

## Decision

**Go owns the product.** Anything a user runs, anything that decides, anything that runs for hours in
a container. The binary is static and ships on `scratch`; that is a property of Go and it is part of
the product's promise.

**Python owns the verifier.** `verify.py` is deliberately a second implementation, in a different
language from the code it judges. A bug in the checkpoint writer must not be able to hide inside the
ruler that measures it. One file, no imports from the product, no runtime dependency on it.

**POSIX shell owns the experiments and the demo.** Scripts may start processes, read JSON, and print
tables. They may **not** do work the product can do itself: the demo writes its fake world with
`ckptctl world --fake`, not with a heredoc, and it did not always.

**Markdown owns the claims.** Every claim in a document either cites a command that proves it or says
plainly that it is not proven. A page with an unbacked number is a bug in the documentation, and the
number is what a reader will remember.

## Consequences

- A change that belongs to the product goes in Go. If it feels easier in shell, that is a signal the
  product is missing a command, not that the shell should grow.
- Python may not gain a second file without a decision record: one file is a ruler, two are a second
  product.
- Shell stays `sh`, not `bash`: the demo has to run on a machine that has nothing but the binary and a
  shell, and `set -eu` plus `printf` covers what it needs.
- Docs are English (CONTRIBUTING §Language). Translation is a separate deliverable, not a pull request
  that half-translates a page.
- A newcomer can place a change by asking which of the four it is, and a reviewer can reject a change
  that is in the wrong one.

## What this does not promise

That these four are permanent. If the verifier ever has to run inside the container, it will need a Go
port, and that will be a decision with its own cost: the independence it was written for would be
gone, and a different kind of independence would have to replace it.

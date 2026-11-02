## Summary

<!-- Describe the behaviour change, not only the files changed. -->

Fixes #

## Why

<!-- Link the issue, the user workflow, or the decision record this follows or creates. -->

## Process

Pick one:

- [ ] Small, self-contained fix — a direct pull request is fine
- [ ] Issue discussed first: #
- [ ] Decision record first: `docs/decisions/NNNN-*.md`

## Scope

- [ ] No change to what the product decides
- [ ] Changes what the product decides, and a decision record says why
- [ ] Changes what a user sees (a line, a receipt, a page)
- [ ] Documentation or developer tooling only

## Validation

Run what the change touches, not everything:

- [ ] `make fast` — format check, vet, build, every unit test, the docs pins
- [ ] `make docs` — the docs pins alone, when the change is only a page
- [ ] `make prepush` — every step CI runs, with the failing one named
- [ ] `make verify` — the contract pins and the recorded decisions
- [ ] `make exp` — when a decision or a write changes
- [ ] `make demo` — when what a user sees changes
- [ ] `docker build -t governor .` — when the image, the Dockerfile, or a dependency changes
- [ ] A new `d-*` case — when the change adds or alters a reason a write happens

If a check was not run, say which and why, in one line.

## Safety

- [ ] No secrets, credentials, or checkpoint data are included.
- [ ] No new dependency: the product is standard library only.
- [ ] Nothing here claims a bound the product does not hold; if it does, the claim says so plainly.
- [ ] A refusal is still reported: nothing that failed is printed as success.

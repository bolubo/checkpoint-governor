# 0028 — The direction is a library, not a platform

Date: 2026-11-02. Status: accepted. Supersedes 0027. Extends 0019 (deployment without a custom
resource) and 0020 (what this is not). Plan: `docs/plan-library.md`.

## Context

The project's goal for now is an open-source community, not revenue. That changes which facts decide the
direction: the scarce resources are attention and contributors, not a payer. Three facts survive from
`docs/references.md` and shape everything below:

1. **The job author does not configure checkpointing.** Where a default exists it was set by a
   framework, blind in time, deadline and write cost. Where it does not, the number is typed into a
   launch script. So the product is a **default value**, and a default values lives where the call
   happens: inside the training loop.
2. **The value is invisible by design.** Our best state is "nothing happened". A component whose value
   is silence can only grow through a reproducible contrast ("40 writes against 1"), not through a
   feature list.
3. **The installer, the payer and the beneficiary are different parties.** 0027 assumed they were one
   "platform". They are not: a cloud vendor earns from writes and from longer jobs. A community cannot
   be built on a party whose ledger may point the other way.

## Decision

**The project is a checkpoint-policy library for the training loop. One small embeddable component,
grown through integrations.** It is not a platform, not a scheduler, not a checkpoint store, not
CLI-first, and not a standards body.

1. **The default is zero periodic writes.** With nothing configured: one write at the end, and one more
   when a stop is announced and it fits. A configured time bound is enforced exactly — the bound is the
   configurator's promise, not ours.
2. **The decision lives in-process.** The Python path is the front door and carries a real policy
   engine, not glue. The Go engine stays for the deployment shape; both are held to the same golden
   policy cases so they cannot drift.
3. **Growth is integration, not features.** One callback per framework (Hugging Face Trainer, Lightning,
   Composer), each small enough to review in a sitting. The operator deployment stays as a deployment
   shape; it is not where new work goes.
4. **The output is a receipt, not an insurance policy.** We report what was written, what it covered and
   what it cost. We do not promise uptime, durability or the machine.
5. **One concept to own: "checkpoint policy".** A time-based input, a written contract, a reference
   implementation. People should be able to name the thing we do without naming our binary.
6. **The refusals stay.** No platform-first work, no certificate product, no adapter zoo, no
   conformance kit, no dashboard (0020).

## Consequences

- **Python becomes the product surface.** The policy core is ported (a small, pure module: bound or
  none, announced stop, end of training, measured write time, step rate), with golden cases shared with
  the Go engine and a test that both answers agree.
- **The repository's first screen changes.** `pip install` plus three lines, then the contrast table;
  `docker build` and `kubectl apply` move to the second screen.
- **`docs/plan-platform.md` and 0027 become history.** The installer/payer analysis in 0027 still holds
  and is worth keeping; its sequencing does not.
- **What we measure**: time from clone to the contrast table, independent integrations, external
  contributors, and whether "checkpoint policy" gets cited in framework discussions. Stars are an
  output, not a target.
- **How this fails**: if hosts will not list a third-party callback and users will not add three lines,
  the operator deployment becomes primary; if a framework ships the same policy itself, the project
  becomes its reference implementation and verifier; if the contrast cannot be seen in two minutes, the
  demo is the thing to fix first.

# Parity with the first edition: three edges, and what we still refuse

This page records the comparison that decided the last batch of work. It is about **posture**, not
line counts: a first edition of a controller establishes what it exports, what it listens to and how
far it can be extended. Ours had the decision core and the delivery plane, and it was missing three
of those edges.

The reference is node-problem-detector `v0.3.0-alpha.0` (2017-01-27), the first edition of the product
we used as the yardstick; the second edition is `v0.3.1`–`v0.6.0`. keda `v1.0.0` and kube-state-metrics
`v0.1.0` were read at the same time for the same question: what does a first release have to show
before a second one is possible.

## The three edges, now closed

| Edge | First edition | This repository, before | This repository, now |
|---|---|---|---|
| Numbers a scraper can read | `/metrics` in Prometheus text | none — a person ran `status` | `ckptctl serve --metrics`, `internal/metrics` |
| An outward event | Kubernetes Events through the API server | none — a person tailed `events.jsonl` | `--webhook`, one POST per changed plan, `internal/notify` |
| A condition an operator adds | a plugin binary with a RPC | none — a fork, or nothing | `.ckpt-governor/conditions.d/*.json`, one file per condition |

Each row is one file, one test and one line of output, which is what `make parity` runs:

```
$ make parity
parity: 3/3 — metrics, notification, conditions from disk
```

The three checks are the real tests, not a checklist: `TestTheExpositionIsValidAndHonest`,
`TestAChangedPlanIsPosted`, and `TestAConditionFromDiskReachesTheDecision`. `make verify` runs
`parity`, so a later change cannot quietly delete one of the edges and keep the page true.

## Measured, not estimated

Files at the moment of the comparison, counted over the tree (`.git` excluded; the references come from
their GitHub trees at the tag, ours from the working tree):

| Repository, tag | files | Go | test files | Markdown (own) | YAML |
|---|---|---|---|---|---|
| checkpoint-governor, `main` (2026-11-01) | 344 | 205 | 114 | 41 | 20 |
| checkpoint-governor, `main` + docs pass (2026-11-02) | 314 | 222 | 123 | 27 | 25 |
| node-problem-detector `v0.3.0-alpha.0` | 1464 | 997 | 8 | 4 (+20 vendored) | 9 |
| keda `v1.0.0` | 145 | 57 | 13 | 12 | 16 |
| kube-state-metrics `v0.1.0` | 1273 | 860 | 1 | 1 (+30 vendored) | 11 |

Read the last two relevant columns, not the first three. The reference repositories' file and Go counts
include their `vendor/` trees (997 of node-problem-detector's 1464 files, 860 of kube-state-metrics'
1273), so those columns are a sense of scale rather than a comparison. The Markdown column counts the
repository's own pages: the 20 and 30 extra files are vendored package READMEs, and counting them is how
a first draft of this table claimed node-problem-detector had 24. It had 4.

The test-file column is the one that matters. The first edition of node-problem-detector had eight; the
repository it was replacing had none it could show. A controller that reports what it did has to be able
to show that it did it, which is why the decision core is 123 test files for 222 Go files.

## What we deliberately do not match

- **No custom resource, no operator** (`decisions/0019-deployment-without-crd.md`,
  `decisions/0020-what-this-is-not.md`). The first edition is a DaemonSet that talks to the API
  server; our input is a directory and our output is a file a job reads.
- **No scheduler.** The first edition does not schedule either; the second one is tempted to. We keep
  one write per volume at a time, which is an I/O interlock and not an admission decision.
- **No plugin binary.** A subprocess with a RPC protocol is a second API surface to version, secure
  and crash-test. A JSON file and one file read is the same capability with none of that, and it is
  bounded on purpose: it adds a name, not a measurement (`decisions/0024-conditions-from-disk.md`).

## What this page is not

It is not a claim that more lines, files or tests is better. It is a record of the three edges we
lacked, the evidence that each is now present, and the gate that keeps the claim honest. The second
edition's theme — a view over many units, and an observer that publishes who may write — is
`supervise` and `decisions/0021-fleet-writes.md`.

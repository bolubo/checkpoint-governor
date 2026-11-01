# 0020 — The accounting is derived, not exported

Date: 2026-11-01. Status: accepted. Applies 0017 and 0019 to the question "how has this deployment
been doing".

## Context

Every neighbour ships a metrics endpoint at their first release: kueue and karpenter both carry a
`/metrics` handler, because an operator with twenty controllers needs one scrape target.

This product is one process beside one job, and the question its operator asks is not "what is the
current rate" but **"what did this deployment do, and what did it cost"** — asked after a run finished,
or after something went wrong, or on a Monday about last week. A scrape endpoint answers the first
question and is silent for the other three: it needs a live process, which is the one thing that is
not there when the question is urgent.

Meanwhile the ledger already exists. It is written every round whether or not anybody watches, it
survives restarts, and it carries the numbers that matter: write costs, the running total, failures,
reconciliations, verification results.

## Decision

**`ckptctl stats --dir` reads the files and prints one page.** No process endpoint, no time series, no
push exporter.

The numbers are the product's own accounting, and they are all about its own behaviour:

| The product counts | The product does not count |
|---|---|
| writes, and what each cost | loss, throughput, tokens, loss curves |
| failures it survived | GPU utilisation |
| plans found on disk that the ledger had not recorded | memory of the training job |
| checkpoints read back, and mismatches | anything the user's job measures |

That table is the line. A metric about the training job belongs to the job's own monitoring; a metric
about **when this process decided to write, and whether the write held** is the product's, because
nobody else can produce it.

## Why not an endpoint

1. **It needs a live process.** The most common time to ask is after the controller is gone. The
   ledger answers then; a scrape target does not.
2. **A scrape target attracts the wrong metrics.** The first contribution to a `/metrics` handler is
   usually a training metric, and `docs/product-v1.md` says plainly that this is not monitoring
   software. A file that cannot hold those metrics keeps the boundary.
3. **It costs a listener.** A controller that opens a port has to justify the port to a security
   review, and this one integrates by reading a directory.

## Consequences

- `ckptctl stats --dir` prints writes (median, total, last), failures, reconciliations, verification
  results, units seen, and the last plan with its age. `--json` for a script.
- The median is reported rather than the mean: one slow write on a busy mount must not describe a
  deployment.
- `reconciled` is called out in words when non-zero, because it is the only number on the page that is
  a fault: it means a plan reached the disk that the ledger never recorded.
- A user who wants a dashboard can read the ledger; a user who wants a time series has to build one,
  and that is deliberate.

## What this does not promise

History. The ledger keeps a bounded window of the last samples and a running total, so "the last
twenty writes" is answerable and "the write cost on the third Tuesday of March" is not. Trend analysis
is a different product, and building it here would make this one keep data forever.

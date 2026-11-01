# 0019 — The deployment surface is a directory, not a custom resource

Date: 2026-11-01. Status: accepted.

Every control plane we measured shipped an install path in its first release. This repository ships
a binary and a Dockerfile, and nothing an operator can apply.

## Context

`git ls-files` finds no manifest and no example:

```
$ git ls-files | grep -Ec '(^|/)deploy/|(^|/)examples/|charts/|kustomize'
0
```

`ckptctl supervise` can drive several checkpoint directories, but only for a fixed number of ticks
(`--rounds`, default 6). It is a demonstration, not a process someone leaves running on a node.

Every input the decision uses is already local: the checkpoint directory, `statfs`, cgroup files, and
two small files the job writes. `docs/conditions.md` names the source of each of the fourteen
conditions. There is no desired state for an API server to store, and no object for another controller
to watch.

## Decision

**The installation is a process, a manifest, and a list of directories — never a custom resource.**
A CRD is added only if at least two of these become true: state must be shared across nodes; another
system must subscribe to our objects; a person must edit our intent with `kubectl`.

## Evidence

### FACT

The first release of each reference project, counted from its git tree:

```
$ gh api repos/kubernetes-sigs/kueue/git/trees/v0.1.0?recursive=1 \
    --jq '[.tree[]|select(.type=="blob")]|length'
131
$ gh api repos/kubernetes-sigs/kueue/git/trees/v0.1.0?recursive=1 \
    --jq '[.tree[]|select(.path|startswith("config/"))]|length'
46

$ gh api repos/kedacore/keda/git/trees/v1.0.0?recursive=1 \
    --jq '[.tree[]|select(.path|startswith("deploy/"))]|length'
10
$ gh api repos/kedacore/keda/git/trees/v1.0.0?recursive=1 \
    --jq '[.tree[]|select(.path|startswith("examples/"))]|length'
3

$ gh api repos/kubernetes/node-problem-detector/git/trees/v0.3.0-alpha.0?recursive=1 \
    --jq '[.tree[]|select(.path|startswith("demo/"))]|length'
8
```

kueue's install surface is 46 files of CRD and kustomize; node-problem-detector's is one DaemonSet
YAML at the repository root plus eight fault samples under `demo/`; keda's is ten files under
`deploy/` and three examples. None of them asks a user to build from source first.

`ckptctl supervise` already holds N directories in one process and one lease per directory
(`internal/supervisor`), so the missing piece is the process lifetime and the manifest, not the
engine.

### REPORT

None. No user has been asked how they install it, so there is no report to cite.

### ASSUMPTION

The first real user applies one manifest to a cluster that already runs training jobs, and reads one
log line. It would be falsified if the first users instead run `ckptctl run` as a sidecar inside their
job pod, in which case the manifest this record adds is the wrong first surface and this record
should be rewritten.

## Consequences

- `deploy/governor.yaml`: one Pod, the checkpoint volume, and `--members-from` reading the governed
  children of that volume. A DaemonSet is deliberately not the first shape — the granularity of the
  product is a directory, and one process holds many of them.
- `ckptctl supervise --forever`: tick until SIGTERM or SIGINT, then print the rollup once.
  `--members-from=<root>` re-reads the directory every tick, so a job that appears joins and one that
  disappears is closed; the line `members: added=1 removed=0 units=3` is the evidence.
- The `--rounds` mode stays for tests and for a one-shot check.
- An acceptance line a person can read: `supervisor stopped · units=3 ticks=12`.
- Deleted: the README sentence that presents `make install` as the only install path.

## What this does not promise

No multi-tenant isolation (filesystem permissions are the boundary), no `kubectl get` view of a
decision, no RBAC integration, and no state shared between nodes. Those are the three conditions that
would make a CRD worth its install cost, and none of them is true today.

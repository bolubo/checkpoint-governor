# The Kueue experiment: what actually gets preempted

`docs/decisions/0021` classifies a cause by **who dies**, and `risk.eviction-likely` guesses from
structure. Both rest on an assumption nobody measured: that a higher-priority arrival takes *some* of
the running units, and that the ones it takes are the cheapest.

This directory measures that on a real scheduler.

## What is measured

**Ground truth only.** Which workloads Kueue actually preempts, how many, and how long it takes. The
governor's own write count is a separate number and needs the observer running in the cluster; it is
not part of these cases.

The two numbers that matter, per case:

| Number | Why |
|---|---|
| preempted count | the fanout: 1 means `one-per-victim`, 4 means the whole node |
| preempted identity | whether the victim is the cheapest, the newest, or something else |

## The cases

| Case | What it adds | What it tells us |
|---|---|---|
| `c0` | 5 holders, nothing arrives | the control: **0 preemptions**. If this is not 0, no other number is usable |
| `c1` | one higher-priority job that needs 0.5 CPU more than is free | one victim, and which one |
| `c5` | a higher-priority job, but `preemption: Never` on the queue | the rule must **not** fire: priority alone is not enough |

## Results, 2026-11-02, Kueue v0.9.0, one kind node

| Case | Preempted | Arrival | What it settles |
|---|---|---|---|
| c0 | **0** | — | the control is clean: five holders admit and nothing is taken |
| c1 | **1** (holder-2) | admitted | one claim takes one unit — `one-per-victim` is the right fanout |
| c5 | **0** | waits: `insufficient unused quota … 500m more needed` | a higher priority waiting is **not** sufficient; the rule must not fire |

Two findings that changed the design (0022):

1. **The victim among equals is not predictable.** Kueue took `holder-2` of five identical holders —
   not the first, not the last. Any rule that grants one of a tied group is guessing, and a wrong guess
   suppresses the write of the unit that dies.
2. **`risk.eviction-likely` fires on "someone higher is waiting", which c5 shows is not the same as
   "somebody will be taken".** The signal has to become "waiting **and** it does not fit".

A note on how not to measure this: counting pod phases reports 0 in c1, because Kueue preempts by
suspending the workload again while the pod is still terminating. The admission state is the fact.

### The waiting job has no pod

The third thing this lab taught, and the one that decides what the observer must read:

```
$ kubectl get pods -l job-name=arrival
No resources found.
$ kubectl get jobs
arrival   SUSPEND=true
```

A job held back for quota is **suspended**, so its pods do not exist. An observer that counts pending
*pods* therefore reads `pendingHigherPriority = 0` in exactly the situation the product exists for: a
higher-priority arrival is waiting and somebody is about to be taken. The fact lives one layer up, on
the Job — and there it has a stable identity (`pendingEvent`, the suspended jobs hashed), which is what
lets one arrival cost one write instead of one per round.

## Running it

```sh
./setup.sh                 # kind cluster + Kueue + queue; idempotent
./run.sh c0                # control
./run.sh c1                # one claim
./run.sh c5                # no preemption allowed
./teardown.sh              # delete the cluster
```

Each `run.sh` prints the case's numbers at the end and leaves the cluster as it found it (holders
deleted, queue recreated).

## Four things this machine needs, and why

The setup script exists because of these, not for tidiness:

1. **`127.0.0.1` does not work here.** WSL's localhost forwarding to the kind node's published port is
   broken; `setup.sh` rewrites the kubeconfig to the control-plane container's IP.
2. **The docker daemon has no egress.** Pulls to `registry.k8s.io` time out, and the two mirrors tried
   (`docker.m.daocloud.io`, `swr.cn-north-4…`) returned 403 and 404. `crane` is a Go program that
   honours the proxy, so it downloads the image and `kind load image-archive` copies it in.
3. **The deployment insists on pulling anyway.** `imagePullPolicy: Always` is patched to
   `IfNotPresent`, or the node re-fetches an image it already has.
4. **`kube-rbac-proxy` cannot be pulled either** (`gcr.io`). The sidecar only fronts metrics; it is
   removed. Kueue does not need it to admit or preempt.

None of these change what is being measured. All four are recorded here so the next person does not
rediscover them.

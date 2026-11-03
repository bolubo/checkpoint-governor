# The gate experiment

Two cluster runs that answer the question decision [0030](../../docs/decisions/0030-the-reclaim-is-gated.md)
asks: does holding the arrival until the victim writes actually buy the write, and does a dead gate
block anything?

The page a user reads is [docs/gate.md](../../docs/gate.md). This directory is the harness.

## Prerequisites

- The kind cluster from `../kueue/setup.sh` (`kueue-lab`), with Kueue v0.9.0.
- `docker`, `kind`, `kubectl`, and Go.
- The cluster must be able to run `busybox:1.36` (already loaded by the Kueue lab) and the image this
  script builds.

## Run

```sh
sh experiments/gate/run-g1.sh     # the hold buys the write
sh experiments/gate/run-g2.sh     # fail-open after kill -9
```

`run-g2.sh` expects `run-g1.sh` to have run once: the node already has the binaries, the image and
the manifests.

### The layout the harness uses

kind cannot publish a host directory into a pod on this machine, so everything lives under `/g1`
**inside the node container**:

```
/g1/world      the observer's files (summary, units, reclaims)
/g1/ckpt/<pod> one directory per governed unit
/g1/bin        ckpt-observer, ckptgate
```

The victim pod mounts `/g1` by `hostPath`. The observer and the gate run inside the node too, because
they need the same directory the pod writes. On a real deployment the gate runs on the host, with the
world on a volume both the observer and the gate can see. The difference is where the files live, not
what the gate does.

The victim is `busybox` plus the two static binaries: `ckptctl` is the controller, `ckpttrainer`
writes real `gen-N` directories with a manifest sealed last. It steps every two seconds and writes
for about 1.5 s, so the numbers below are not a model.

## Measured, 2026-11-03

```
                                          no gate      gate
victim's newest checkpoint at death       step 7       step 7
victim's step at death                    13           7
lost                                      6 steps      0 steps
```

G1:

```
gate       sealed at step 7 after 5s: releasing the arrival
submitted  default/arrival
intruder waited 5s · victim lost 0 steps
```

G2:

```
gc         default/victim-qk5vw held for default/arrival: past its deadline, never released
gc         1 marked uncovered
```

The 5 s wait is the probe cadence plus one write. The first run of this harness waited 55 s because
the probes hashed the checkpoint directory's basename instead of `--unit`, and the 60 s cooldown
applied to a new reclaim; both are fixed.

## What the harness does not prove

- One node, one queue, one pod: no scheduler spread, no cohort, no tied victim group.
- The 55.4 s wait is the victim's own cadence plus a write cooldown; a longer deadline is not a
  better gate, and a shorter one would have released without a seal.
- The gate only covers submissions through it. The direct submission in G2 is the control arm, and
  it lost the steps the gate would have saved.

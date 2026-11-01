# Deploying it

One manifest, one volume, one directory root. The process watches every job under the root and
publishes a plan into each job's own `.ckpt-governor/`.

## The three surfaces

| Where | Command | What it is for |
|---|---|---|
| a laptop or a single job | `docker run --rm -v /ckpt:/ckpt ckpt-governor:local run --dir /ckpt` | the product, no discovery |
| a node with many jobs | `kubectl apply -f deploy/governor.yaml` | one process over a shared volume |
| development | `make install` | the two binaries on `PATH` |

## Two clusters

`--members-from` takes one or more roots, and `cluster=root` names the world each root reads:

```bash
ckptctl supervise --members-from=kind-a=/ckpt-a,kind-b=/ckpt-b --forever
```

Each unit reads `<root>/world/<cluster>/`, and the rollup says how many clusters it holds
(`supervise: units=2 clusters=2 ...`). One process per cluster is still the common deployment; naming
two roots is for a machine that reaches both (`docs/decisions/0020-what-this-is-not.md`).

## What the manifest assumes

- **A volume at `/ckpt`** (a `hostPath` in the shipped file). Each immediate child that holds a
  `.ckpt-governor/` directory is a unit; everything else under the root is ignored.
  `grep members-from deploy/governor.yaml` shows the flag that does the discovery.
- **The image is `ckpt-governor:local`.** Build it (`docker build -t ckpt-governor:local .`) and load
  it into the cluster, or replace the tag with a released one. `imagePullPolicy: IfNotPresent` is
  what makes the local tag work.
- **A non-root user (65534) and a read-only root filesystem.** The only writable path is the
  checkpoint volume; that is where the plan, the ledger and the events live.

## What it prints

```
supervisor stopped · units=3 ticks=12
/ckpt/job-a    save    queued=1 processed=1 waiting=0 processing=0 dropped=0
supervise: units=3 sealed=1 decided=3 save=2 skip=1
```

The stop line is the acceptance of `--forever`: the process runs until SIGTERM, then prints one
rollup. Nothing else is required to prove it is alive; the per-round lines and the plan files in each
directory are the evidence.

## What this does not do

No CRD, no `kubectl get` view, no RBAC, no per-tenant isolation: the filesystem's permissions are the
boundary (`docs/decisions/0019-deployment-without-crd.md`). If you need a second replica or state
shared between nodes, this manifest is the wrong shape and the decision record says why.

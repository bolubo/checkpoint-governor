# Examples

Two manifests a user copies, and the three situations they correspond to. The local claims behind
them are already executable: `make scenarios` prints one line per situation and fails if a line does
not match.

| Example | What it shows | Where the claim is proven |
|---|---|---|
| `single-job.yaml` | one job, the governor as a sidecar, a 30m window | `make scenarios` (`user-stop`, `full-disk`) |
| `gang.yaml` | two ranks, one volume, one generation | `go test ./internal/unit` (members → generation) |
| — (no manifest) | a preemption: `kubectl delete pod train-1` | `make exp` (`d-preempt`), `make scenarios` (`eviction`) |

## Preemption is a delete

There is no third manifest because the platform's announcement is the delete itself: the governor
sees `deletionTimestamp` in the world record and asks for one write before the pod goes.

```bash
kubectl delete pod train-1 --grace-period=30
kubectl logs train-1 -c governor | tail -3     # save risk.preempt → sealed → may-exit
```

The fit test is what makes it honest: if the remaining grace period is shorter than one write, the
line says `no-time` and the loss is reported instead of hidden.

## Running them without a cluster

Both manifests use `ckpt-governor:local`, so a cluster needs the image loaded (`kind load
docker-image ckpt-governor:local`) or the tag replaced with one from `RELEASE.md`. Without a cluster at
all, `make demo` runs the same three situations against two real directories.

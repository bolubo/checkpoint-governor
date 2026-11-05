# v0.27.0 — what the volume promises, and what a workload-level hold waits for

Status: release, 2026-11-05. The number is the tag, the wheel and `ckpt_policy.__version__` (decision
0029). This release turns two assumptions into measurements: a seal is only a seal if the disk reads
back, and a gate that holds a multi-pod job must wait for the job's whole copy.

## 1. The volume is measured, not assumed

```bash
ckptctl doctor --storage /ckpt
```

```
storage /tmp
  filesystem  tmpfs (0x1021994), local
  fsync file  ok      6µs      4096 bytes
  rename      ok      12µs     atomic: .payload.tmp -> payload.bin, same directory
  visible     ok      5.978ms  pid 831526 read 4096 bytes at once
  size/tail   ok      39µs     size 4096, tail sha256 725bcd6c66d0
  fsync dir   ok      3µs      directory entry synced
  class       local
  promise     process death: a sealed generation survives this process; not the node, not the volume
```

The three tiers are the promise, not the mount's name:

| class | promise |
|---|---|
| `local` | survives the process; not the node, not the volume |
| `shared-safe` | survives the process **and the node**; not the volume |
| `shared-unsafe` | nothing beyond what is readable now; do not resume across pods or restarts from it |

The same machine, a network mount, a different tier:

```
storage /mnt/c/tmp/ckpt-probe
  filesystem  9p (0x1021997), network
  rename      ok      8.568ms  atomic: .payload.tmp -> payload.bin, same directory
  visible     ok      9.599ms  pid 1031197 read 4096 bytes at once
  fsync dir   ok      3.636ms  directory entry synced
  class       shared-safe
```

`docs/storage.md` carries the full contract, including what each tier refuses to promise.

## 2. The seal reads the disk back

`Governor.sealed` and `ckpttrainer --contract` re-open every shard before writing the manifest: size,
then the declared tail hash, once, with one retry. A shard that vanished, changed size or lost its tail
is not sealed:

- no `manifest.json`, no `resume.json`;
- the attempts and the shard names go to `.ckpt-governor/seal-failed`;
- `ckptctl resumable` answers "no copy" rather than `world N/N` over a generation that was never sealed.

The gate uses the same check before it releases an arrival: a manifest that exists is not the same
fact as a copy that is whole.

## 3. A workload, one hold, every rank

`ckptgate submit` now holds the **workload** a preemption takes:

```
gate       default/arrival needs 2000m; lab is 1000m short on kueue-lab-control-plane
victims    default/victim (kueue-lab-control-plane, priority 50, size 2000m)
deadline   1m30s
gate       sealed at step 9 after 300ms: releasing the arrival
submitted  default/arrival
```

- The observer records each pod's `workload`; the gate writes one reclaim record per workload, naming
  its pods. A two-pod Job is one hold, not two.
- Release needs all of it: the manifest appeared after the hold, every shard reads back, `sealedAt` is
  not older than the hold, and the manifest names a shard for every rank of the workload. A seal that
  names `rank-0` of a two-rank workload prints the missing rank and keeps waiting.
- A timeout is fail-open and is recorded as `uncovered`: the write was asked for and never promised.
- A multi-pod victim on a per-node `hostPath`, or whose pods resolve to different directories, is
  refused with the reason: there is no shared copy, so there is no seal to wait for.

Single-pod output is unchanged, down to the comma.

## 4. The shared volume stops paying per step

`Governor(storage="shared")` writes the progress cell at most once per interval (default 1s), and
`sealed`, `ending` and `close` flush the last held step. The controller still sees liveness at the
resolution it decides at; a fast step on a network volume no longer costs a metadata operation.

## 5. Measured

The two-pod harness (`hack/kind-multipod-gate.sh`, kueue-lab, one kind node, a PVC-backed root):

```
sealed both ranks · arrival created · victim lost 0
```

The victim is the real `ckpt_policy` client, torch-free: two indexed pods, one shared root, a file
barrier, rank 0 seals after both shards exist. The harness also checks that the world holds exactly
one reclaim record for the workload.

```bash
make fast                 # fmt, vet, build, Go tests, docs links
make py                   # 60 tests
make test-claims          # claims: 44 · tests found: 44 · missing: 0
sh hack/storage-probe.sh /path/to/mount
sh hack/kind-multipod-gate.sh
```

## 6. What this release does not claim

- **NFS/Lustre measured.** The network-tier evidence in `docs/storage.md` is WSL's 9p passthrough, not
  NFS. `hack/storage-probe.sh` is what a user runs on their own mount; the page says the result is not
  claimed until someone pastes it.
- **A multi-node shared root.** The two-pod harness runs on one kind node with a PVC; it proves the
  gate's workload logic, not cross-node volume semantics.
- **Gradient sync across ranks.** DDP/FSDP is still the framework's job; the real multi-GPU run is the
  next roadmap item.
- **Object storage.** An S3 adapter has different rename semantics and would need its own contract.

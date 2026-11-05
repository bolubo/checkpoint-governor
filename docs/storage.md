# What the volume promises

A checkpoint is sealed by writing every shard, syncing it, and renaming the manifest. That protocol is
the whole of the durability story, and it only means what people assume if the filesystem underneath
does what the idiom assumes. On a laptop it does. On some network mounts it does not, and the copy that
looks sealed is the one a restore cannot use.

`ckptctl doctor --storage` measures the volume instead of assuming it. It runs the four syscalls the
seal depends on, in a temporary subdirectory it removes afterwards, and prints what happened:

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

It runs before a deployment, with or without a checkpoint directory (`--dir` and `--storage` may be the
same path). The exit code is the verdict: `0` for `local` or `shared-safe`, `1` for `shared-unsafe`,
`2` when the directory cannot be probed at all.

## The three tiers

| class | what was measured | the promise |
|---|---|---|
| `local` | a local filesystem, every check passed | **process death**: a sealed generation survives this process; not the node, not the volume |
| `shared-safe` | a network filesystem, every check passed including the directory sync | **process and node death**: another client on the same root reads it after the writer dies; not the volume |
| `shared-unsafe` | any check failed, or a network mount that failed the directory sync | **nothing beyond what is readable now**: do not resume across pods or restarts from this volume |

The tier is decided by the checks, not by the mount's name. A local disk that fails the rename check is
`shared-unsafe` too, because the word is about the promise, not the hardware. Every failed line carries
the reason; there is no line that says only "unknown".

## What each check means for the seal

- **fsync file** — the shard survives the process that wrote it. Without it, a seal can point at a file
  the page cache still owes.
- **rename** — a same-directory rename replaces the target atomically. The manifest is one small file
  renamed after every shard exists, so a reader sees either no manifest or a complete one.
- **visible** — a second process reads the just-renamed file at once. Page cache in the writer would
  answer a different question; this asks what a client that just received the plan will see.
- **size/tail** — what the reader gets is the bytes that were written, not a short file or a torn tail.
  `TailSHA256` is the field that makes this check cheap enough to run on every save.
- **fsync dir** — the directory entry survives the node. A shared mount needs it before `shared-safe`;
  a local disk does not need it to promise process death.

## The writer's side: read back before sealing

`Governor.sealed` (Python) and `ckpttrainer --contract` (Go) now re-open every shard before writing the
manifest: size and the declared tail hash, once, with one retry. A shard that vanished, changed size or
lost its tail does not get sealed; the failure is recorded in `.ckpt-governor/seal-failed` with the
attempts and the shard names. `ckptctl resumable` then reports no copy, instead of a `world N/N` over a
generation that was never sealed.

This is the one ordering the product insists on: **the manifest is the promise, so the disk is asked
before the promise is made.** A writer that seals an unreadable shard records a copy no restore can use.

## What we refuse to promise

- On `shared-unsafe`, nothing is promised across pods or restarts. The files are still readable now, and
  a single-node job may still resume from them; the gate and the multi-rank pages say not to rely on it.
- A `local` volume is not promised across node death. A pod with a per-node `hostPath` checkpoint root
  is a single-node promise whatever the job's `parallels` say.
- No tier is promised across volume loss. Redundancy and snapshots belong to the storage system, not to
  this contract.
- Object storage is not covered. An S3 adapter has different rename semantics and would need its own
  contract; this page measures POSIX mounts only.

## All ranks must see one root

A multi-rank save writes `rank0.pt`, `rank1.pt`, … into one generation directory, and rank 0 seals
after the barrier. If the ranks' directories are not the same directory, rank 0's manifest describes
files that rank 1 cannot read, and no seal can make the copy whole.

- Use one RWX volume (or one shared filesystem) mounted at the same path in every rank and in the
  controller.
- A per-node `hostPath` is acceptable only for a single-pod job. The gate refuses a multi-pod victim on
  one ([gate.md](gate.md)), because there is no seal to wait for.

## Observed

`tmpfs` (the example above) is a development machine. A network mount on the same machine reads
differently — this is WSL's 9p passthrough, and it is reported as a network filesystem:

```
storage /mnt/c/tmp/ckpt-probe
  filesystem  9p (0x1021997), network
  fsync file  ok      7.133ms  4096 bytes
  rename      ok      8.568ms  atomic: .payload.tmp -> payload.bin, same directory
  visible     ok      9.599ms  pid 1031197 read 4096 bytes at once
  size/tail   ok      5.862ms  size 4096, tail sha256 725bcd6c66d0
  fsync dir   ok      3.636ms  directory entry synced
  class       shared-safe
  promise     process death and node death: another client on the same root reads it after the writer dies; not the volume
```

That is one local tier and one network tier differing on the same machine. A real NFS/Lustre run
belongs here too, pasted from `hack/storage-probe.sh`; until someone runs it on their mount, this page
does not claim a measured result for those filesystems. 9p is not NFS, and the difference is stated
rather than blurred.

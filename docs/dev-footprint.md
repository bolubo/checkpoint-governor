# Footprint: the development machine, and what an experiment may leave behind

This page and `docs/retention-budget.md` are **two different designs**. Do not mix them.

| | what is kept on the customer's volume | what an experiment keeps on this machine |
|---|---|---|
| what grows | the checkpoints the customer writes | the clusters, images, volumes and caches I create |
| who pays | the customer's disk | my 1007 GiB root disk |
| who sets the bound | the retention rule (keep, plus the oldest step) | **the experiment must give the resources back** |

The two do not conflict. They are written down separately because they converge in opposite ways:
the customer's side by keeping fewer copies, this machine by tearing things down.

## 1. What this machine held (measured)

```
local volumes   26      39.61 GB   <- the largest item; 22 still in use
images          23      14.83 GB   <- 11.25 GB reclaimable (75% is dead)
kind clusters    7                 <- seven clusters from one repository
Go build cache          12 GB
/tmp                    64 MB      <- this one was healthy
root disk       318G of 1007G used
```

**The two things `du` cannot see are the larger half.** Walking directories with `du -sh` finds waste
in the 60 MB range; volumes and images are in the 50 GB range, inside docker's own storage. So
**measure `docker system df` first, and the directories second.**

## 2. Three rules, one per lifetime

**1. One run (seconds).** Every temporary directory comes from `mktemp -d` with a `trap` that removes
it. This was already true: the matrix and the demo both do it.

**2. One experiment (minutes). The cluster's life must close inside the same script.**

```
create -> load image -> run -> delete
```

An interrupted exit must delete too (`kind delete cluster` in the trap). One rule:

> **An experiment that leaves a cluster behind is a failed experiment**, whatever it measured.

The reason is not tidiness. With seven clusters left, I could not tell which one was current and which
was three days old, so **the next experiment started with archaeology** — and archaeology was wrong
more often than my code was (today's F7 and F10 came out of it).

**3. Once a week (minutes).** `hack/footprint` reports the account, and anything over its bound is
reclaimed:

| item | bound | when it is over |
|---|---|---|
| docker volumes | 60 GB | `docker volume prune` (unowned only) |
| docker images | 20 GB | `docker image prune -a`, keep the base image |
| kind clusters | 2 | delete the unused ones; names carry a date |
| Go cache | 15 GB | `go clean -cache` |
| /tmp | 2 GB | delete anything older than a day |

## 3. The WSL disk: it is already sparse, deleting inside is enough

This machine's vhdx runs in **sparse** mode: deleted blocks go back to Windows, so `Optimize-VHD` is
not needed.

Cleaning is therefore one step: **delete inside**. This is written down because it removes a whole
class of "clean up, then shut down and compact" work, and that class is what makes people stop
cleaning at all.

If the disk is ever replaced with a non-sparse one, the compaction step comes back. The test on the
Windows side is whether the vhdx shrinks when files are deleted.

## 4. Memory

Each kind control plane is one container, roughly 1–2 GiB. Seven clusters are up to 14 GiB — but
several of them are `Exited`, which **holds no memory, only disk**.

Memory is therefore easier than disk: **stopping is the same as giving it back.** One rule:

> A cluster nobody uses is either `docker stop` or `kind delete`. It is never left `Running`.

## 5. What this design becomes

- `hack/footprint`: one line per item (volumes, images, clusters, cache, vhdx), **non-zero exit** when
  over a bound, so it can go in `make`;
- `kind delete` in the trap: the cluster script removes what it created, without depending on anyone
  remembering;
- cluster names carry a date (`kueue-1102`), and the same name is deleted before it is created.

## 6. The first run (landed)

`hack/footprint.sh` printed:

```
kind clusters          8  <-- over budget (max 2)
       - ckpt-exp  k17921  kueue-lab  ra-prod  ra-t67  ra-user  resume-admission ...
docker volumes         39.6 GB
docker images          14.8 GB (11.25 GB of it marked reclaimable)
go cache + mod         12.2 GB
/tmp                   66 MiB
```

**That one run proved the premise of the design:** what was over budget was the number of clusters
and docker's storage, not files in directories.

`docker image prune -f` (dangling layers only) reclaimed **0 B**. The 11.25 GB marked reclaimable
needs `-a`, and `-a` also removes the base images other repositories are still using. So this stays a
human decision: the script reports and does not act. A footprint tool that deletes other people's
images is a tool nobody runs twice.

**The clusters were left alone.** `ra-prod`, `ra-t67` and `resume-admission` belong to other
repositories and their authors. The script marks them in red and stops there.

**The three cases this page listed** — filling a 32 MiB volume (3.1), the long-run trash peak (3.2),
and ledger truncation (2.4) — landed afterwards; `docs/retention-budget.md` §6 records what they
showed. What is still missing, on both pages, is one hours-long run on a real machine.

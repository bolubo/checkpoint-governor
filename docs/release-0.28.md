# v0.28.0 — thirty seconds to see it, one command to register it

Status: release, 2026-11-05. The number is the tag, the wheel and `ckpt_policy.__version__` (decision
0029). This release does not add a harder guarantee; it makes the guarantees easy to see, machine
readable, and available to the agent a user already has open.

## 1. Thirty seconds, no cluster

```bash
pip install ckpt-policy
ckptctl demo
```

```
demo /tmp/ckpt-demo: 2 ranks, gen-0001, step 300, storage local
fault: rank1.pt deleted — the kill landed between the shards and the manifest
status      newest sealed step 300 · writing none
resumable   not resumable: rank 1 shard missing
verify      broken: shard rank1.pt is missing
evidence    1 problem · storage local (process death)
next        ckptctl demo --repair --dir=/tmp/ckpt-demo
```

`ckptctl demo --repair` seals it again; the same commands answer `resumable (world 2/2)` and
`verified`. Four faults (`kill`, `torn`, `short`, `clean`) are real file operations on a real
generation, not a scripted transcript. The demo refuses a directory it did not create, and `--clean`
requires its marker, so it cannot delete a production checkpoint ([docs/demo.md](demo.md)).

## 2. One entry point for a deployment

```bash
ckptctl setup --local --dir=/ckpt
ckptctl setup --check --dir=/ckpt --queue=lab
ckptctl setup --write=/tmp/governed --dir=/ckpt --queue=lab
```

`--local` measures the volume and prints the four lines of glue. `--check` reads kubectl (only reads):
Kueue, capacity, the volume, the storage class, `ckptgate`, and the controller's published plan, each
as `ok`, `blocked` or `unknown` with a reason and the command that changes it. `--write` emits
`governed-job.yaml`, `submit.sh`, `agent-mcp.json` and a three-command README; it does not create a
CRD, an operator, or a webhook, and it applies nothing ([install.md](install.md) §3).

## 3. An agent entry, read-only

```bash
ckptctl setup --agent=claude --apply --project=. --root=/ckpt
```

The command writes `.mcp.json` and the skill, makes a backup of every file it replaces, and is
idempotent. `ckptctl mcp --root=<dir>` serves six tools over stdio — `status`, `resumable`, `verify`,
`explain`, `storage`, `evidence` — and has no write tool. A path outside the roots, including through
a symlink, is refused with the reason ([agents.md](agents.md)).

## 4. One evidence bundle

`ckptctl evidence --dir=…` and `--json` on `explain`, `audit`, `verify` and `doctor` print the same
decision in one schema-1 object: status, resume, newest copy and its shards, attempts, storage, and a
closed set of problem codes. The bundle does not re-judge: a test compares it with the command it
assembles ([evidence-schema.md](evidence-schema.md)).

## 5. Measured

- `ckptctl demo`: four scenarios, milliseconds, no GPU or cluster; the golden files are in
  `cmd/ckptctl/testdata/demo/`.
- The two-pod gate harness from 0.27 still prints `sealed both ranks · arrival created · victim lost 0`;
  `docs/showcase/gate.txt` is the transcript.
- The S1 Hugging Face run on this host: `trained 200 steps in 4.5s`, `checkpoints: 1`,
  `resumable (step 200, …)`; `docs/showcase/hf.txt`.
- `make py` runs 60 tests; `make test-claims` prints
  `claims: 51 · tests found: 51 · missing: 0`; `make mcp-test` covers the handshake, all six tools, the
  path guard and the config writers.

## 6. What this release does not claim

- **A real two-GPU DDP/FSDP run.** Still gated on a two-GPU host and on usage; the harness is not
  shipped as a promise. It is the first item of the next roadmap.
- **PyTorchJob/MPIJob victims, Ray/Slurm, object storage.** Not in this release; the gate still reads
  `batch/v1 Job`, and the storage contract measures POSIX mounts only.
- **Lightning and Composer recipes.** Their callbacks are supported and tested; the copyable loops are
  still open (#202, #203), and this release says so rather than shipping a page nobody ran.
- **A cluster this release applied for you.** `setup` reads and writes files; it never mutates a
  cluster object.

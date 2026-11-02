# Security policy

## Report privately

Use GitHub's private advisory form:
<https://github.com/bolubo/checkpoint-governor/security/advisories/new>. It is visible to the
maintainer only until a fix is published, and it gives us a thread for the fix and the credit.

Please do not open a public issue for a vulnerability. If the form is unavailable, contact
`@bolubo` through their GitHub profile.

## What is in scope

This product reads a checkpoint directory and writes a state file. The claims that can hurt are
about boundaries, so these are the reports we want:

- a path outside the directory that `ckptctl run` reads, writes, or follows through a symlink;
- a name in a plan, a state file, or `gen-NNNN/manifest.json` that escapes its directory;
- a write that modifies or deletes an existing checkpoint, though the product promises it never does;
- `install.sh` installing something whose checksum it did not verify, or verifying the wrong thing;
- a plan or a file written by one unit that another unit reads as its own;
- a secret, a token, or checkpoint data that reaches a log, a receipt, or a response.

## What is not in scope

- A bug in the training framework, the container runtime, or Kubernetes itself.
- Timing differences that need an attacker already running code in the same directory.
- The contents of a checkpoint. We verify that it landed and that it can be stepped once; we do not
  inspect what it contains.
- Denial of service by filling the disk. The product reports the disk; the operator decides the quota.

## What to expect

One maintainer, best effort. We aim to reply within seven days and to say plainly whether the report
is accepted. There is no promise of a fix date, and no bounty.

When a fix ships, the advisory is published with the credit you ask for, and `CHANGELOG.md` names the
release. A report that we cannot reproduce is closed with the reason, not left open.

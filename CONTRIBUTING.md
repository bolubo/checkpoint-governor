# Contributing

## Start here

- **Pick an issue.** `good first issue` is the entry point; every issue states a done-when. A comment
  before you start keeps two people off the same change.
- **Read `docs/index.md`** to find the page your change touches, and `docs/decisions/` to find out why
  the current behaviour is what it is.
- **Run `make fast`** while you work (format check, vet, build, every unit test, the docs pins).
  `make docs` is the same gate for a page-only change, and `make prepush` is exactly what CI runs.
- **Send one pull request per batch, not per file.** The run is what costs, and auto-merge is armed
  when the pull request opens, so nobody sits watching a check.
- **A change to what the product decides starts with a decision record** (`docs/decisions/`), not with
  code. If you are not sure whether yours is one, ask in
  [Discussions](https://github.com/bolubo/checkpoint-governor/discussions) before writing either.

The rest of this page is the style the review will hold you to. It is short on purpose.

## Language

- **English only** in code, comments, documents, commit messages, identifiers, and file names.
- **Plain words.** Describe what happens. Say "write a checkpoint", not "persist a recovery
  artifact". Say "the controller stopped", not "the governor lost its lease".
- **Define a term once**, where it first appears, and then use it consistently.
- **No internal slang, no metaphors used as names, no marketing adjectives.** Words like "smart",
  "seamless", "powerful", "robust" are removed on sight.
- **Short sentences, one idea each.** If a sentence needs a comma to carry two ideas, it is two
  sentences.
- **Comments explain why.** The code already says what. A comment that repeats the code is deleted.

## Naming

- Packages are named for what they do: `policy`, `observe`, `advise`.
- Files are named for their content. `helpers.go`, `utils.go`, `common.go` are not allowed.
- The only abbreviation allowed is `ckpt` in binary and directory names. Documents spell
  "checkpoint".
- A name that needs a comment to be understood is the wrong name.

## Layers

Two boundaries decide where a change belongs, and both are checked in review.

- `internal/policy` is the algorithm. Experiments are expected here.
- Everything else is delivery: reading the directory, writing the state file, wiring the controller.
  It changes only when the interface changes.

The test: **replacing the algorithm must not touch any other file.** If it does, the interface is
wrong, not the algorithm.

The second boundary is as strict. The arithmetic that prices a decision (`internal/bound`,
`internal/audit`) and the simulator (`internal/simulate`) never decide anything: they may be deleted
without changing a single write. Check that property before merging into them.

**The policy never reads the clock, the disk, or the cluster.** Everything it needs arrives in its
input. A function that reaches for the wall clock cannot be replayed, and a decision that cannot be
replayed cannot be argued with.

## Numbers

- A number in a document comes from a test, a trace, or a measurement, and names its source.
- A number that is a target rather than a measurement is labelled as a target.
- A missing measurement is written as unknown. It is never written as zero.

## Tests

- The policy is tested with properties over generated traces, not only with examples.
- Comparisons are made by replaying the same trace through several policies.
- Any test that needs a cluster is separate from the unit tests and runs once per change.

## Documents

The document set is organised by the question a reader has, and each answer lives in exactly one
place.

- **One subject, one page.** `docs/policy.md` says how a decision is computed; `docs/algorithm/` says
  what must be proven about it and how it is measured. Neither copies the other's tables.
- **A new page replaces something, or is a decision record.** A page that restates an existing one is
  deleted rather than merged.
- **Work in flight belongs in the tracker, not in `docs/`.** Not allowed: per-feature documents,
  status documents, meeting notes, plans that duplicate the README, and experiment logs treated as
  documents. Experiment output lives in `experiments/`.
- If two documents describe the same thing, one of them is deleted. This rule exists so the set
  cannot grow without a reason.

## Pull requests

`main` is protected by the `verify` check. Nothing goes straight to `main`: a change arrives on a
branch, as a pull request, and is squashed when the check is green.

**One pull request per batch, not per file.** Every push runs the whole CI — tests, experiments, demo,
image — and the run is what costs. Related files go in one branch and one pull request. A pull request
that exists only to see whether one file compiles is a CI run spent on nothing.

**Auto-merge is armed when the pull request is opened**, so nobody sits watching a check:

```
gh pr merge <n> --auto --squash
```

If GitHub reports `mergeable_state: behind`, the branch is **rebased** onto `main`, never merged
forward: a merge commit inside a squashed history is noise, so `update-branch` (which merges the base
into the branch) is not used here. The branch is rebuilt instead — `main`'s tree, the branch's files
written into it, one commit with the branch's message, and the branch ref moved to it:

```
BASE=$(gh api repos/bolubo/checkpoint-governor/commits/main --jq .sha)
TREE=$(gh api repos/bolubo/checkpoint-governor/commits/main --jq .commit.tree.sha)
# PUT each file as a blob, POST a tree with base_tree=$TREE, POST a commit with
# parents=[$BASE] and the branch's message, then move the ref:
gh api -X PATCH repos/bolubo/checkpoint-governor/git/refs/heads/<branch> \
  -f sha=<commit> -F force=true
```

With a working tree and a reachable remote, the same result is `git fetch && git rebase origin/main
&& git push --force-with-lease`. Both keep one commit; neither leaves a merge commit behind.

Watching CI is not a job. The check is the gate, and the merge happens without anyone in the room.

## Commits

- English, one milestone per commit.
- The message states what changed and the number that proves it.
- Never commit generated output that a command can reproduce.

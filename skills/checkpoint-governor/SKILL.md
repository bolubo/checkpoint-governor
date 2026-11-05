# checkpoint-governor

Use when a training run's checkpoint cannot resume, when `status` looks wrong, or when a volume's
durability is in question.

## Steps

1. Call the `evidence` tool for the directory. It carries status, resume, verify, attempts, storage
   and problems in one bundle.
2. If `resume.state` is `not resumable`: report `resume.line`, `resume.missingRank` and the first
   `problems[].code`. Do not guess a different reason.
3. If `newest.verified` is false: report the shard name and whether it is missing, short, or has a
   different tail hash.
4. If `storage.class` is `shared-unsafe`: say the copy promises nothing across pods or restarts.

## Rules

- Read-only: never edit a checkpoint, a manifest, or a plan.
- If a field says `unknown`, say unknown and name the reason; never fill it in.
- Prefer the evidence bundle over prose you remember from a different version.

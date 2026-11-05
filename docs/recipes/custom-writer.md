# A writer we have never heard of

The governor does not have to know your checkpoint format. It has to know where the copy is and how
to ask your writer what it left. Both are declared in `.ckpt-governor/layout.json`:

```json
{
  "glob": "step-*",
  "step_regex": "step-([0-9]+)",
  "declaration": "resume.json",
  "probe_cmd": "python3 my_writer.py --describe {dir}"
}
```

- `glob` and `step_regex` find the copies and their step. A directory that matches but has no
  declaration is a write in flight, not a copy.
- `declaration` is the file your writer puts beside the copy, with the schema in
  [resume-contract.md](../resume-contract.md) §1.
- `probe_cmd` is optional. It is run when there is no declaration; it must print one JSON declaration
  on stdout and nothing else. It gets five seconds.

Then the usual three commands answer:

```bash
ckptctl resumable --dir=/ckpt/job
ckptctl evidence --dir=/ckpt/job --json
ckptctl drill --dir=/ckpt/job --cmd="python3 my_writer.py --load {dir}"
```

A writer that records its ranks in shard names (`rank0.pt`, `rank-00.bin`) gets the completeness check
for free. A writer that does not leaves the question unanswered, and the verdict says so instead of
inventing a member.

The six rules a writer must keep are in [integration-template.md](../integration-template.md); the
verdict it will get is in [resume-contract.md](../resume-contract.md).

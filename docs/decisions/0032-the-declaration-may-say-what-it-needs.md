# 0032 — The declaration may say what the job needs

Date: 2026-11-05. Status: proposed. Extends 0026 (resumability is a verdict) without reopening it.

## Context

0026 fixed the shape: the writer declares, we judge, and the verdict never reaches the policy. Its
file said what the copy held, but the ruler was ours: five states, always. That was wrong for every
writer that is not us.

- A Hugging Face Trainer checkpoint never holds a data-loader position: the dataset is re-iterated and
  skipped. Judging it against a dataloader its writer never had refused every copy it wrote.
- Lightning's `.ckpt` and a Composer episode are files, not directories. A declaration inside a
  directory could not describe them.
- A copy written for eight ranks and later read by two is a world change, but the declaration said
  `world.ranks: 1` because the manual `declare` command had no way to say otherwise.
- A newer writer may add a field an older reader cannot understand. Reading it as an older file is
  worse than refusing it.

## Decision

**The declaration may state its own ruler, its own artifact, its own caveats, and the build it needs.
All of it is optional, and absence means exactly what it meant before.**

1. **`requires` is the ruler.** An absent list is the default five (model, optimizer, scheduler, RNG,
   dataloader), which is what every file written before this field said. A framework declares four.
   Only the declared set can make a copy unresumable; a `false` outside it is a fact, not a refusal.
2. **`target` names the artifact.** `"."` is the directory the file sits in; a sibling file name is
   Lightning's `.ckpt` or a Composer episode, and the file is then `<artifact>.resume.json`. A path is
   refused: one declaration describes one copy.
3. **`caveats` are facts that do not change the verdict.** Today the closed set is one value,
   `data-position-not-saved`; a copy with it is resumable, and the user is told the epoch will replay
   or restart.
4. **`min_reader` is the build the file needs.** A lower build refuses with the upgrade named rather
   than misreading a newer file. The engine and the wheel ship the same number (0029).
5. **`ext` is the only extension point.** Readers ignore its contents and record that it was there;
   every other unknown field remains an error, because a typo must not travel through a restore.
6. **The finder reads the layout, not only `gen-*`.** One finder recognises `gen-N/`, `step-N/`,
   `checkpoint-N/`, `*.ckpt`, a Composer episode, and a user's `layout.json`. Preference is fixed and
   a half-written directory is never counted as the newest copy. A copy nobody described is
   `unknown`, never "not resumable".
7. **The plan schema stays 2 and the declaration stays schema 1.** No new condition, event, metric,
   exit code, or policy input. This is a wider ruler, not a new mechanism.

## Consequences

- **Old files keep their old answer.** A declaration without these fields produces the sentence it
  produced before; the default ruler is internal, not new output.
- **Framework writers get one line each.** The adapters write the file beside every save, and
  `ckptctl declare` does the same for copies that predate the integration.
- **A failure is checkable.** `make test-claims` names a test for each promise, including the ruler,
  the target validation, the caveat line, and the version refusal.
- **The next extension starts here.** A writer that needs a sixth state, or another caveat, adds it to
  this record first; the closed sets do not grow by accident.

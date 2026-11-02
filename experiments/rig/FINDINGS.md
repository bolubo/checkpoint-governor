# What the real runs taught us

The incidents themselves are in the git history and the decision records. What belongs here is only what is
**still being paid for**: the rules that became tests, or that should have.

## Rules this cost us

1. **Test data must come from the real producer.** Fixtures authored by the same mind as the reader encode the
   reader's assumptions — nine of ours did, and none caught a writer naming its directory differently.
2. **Both ends must contradict each other on one screen.** A seam bug is invisible when each side prints into
   its own green report; it is obvious when both lines land in the same log.
3. **Cross the seam on the day you change one side.** "End-to-end later" is how a rename sat unnoticed while
   every unit test passed.
4. **A suppressed error is a suppressed result.** `|| true` is a judgement per line, never once at the top.
5. **A status read through an interpreter you have not tested is a guess.** `$?` through a wrapped shell
   printed 0 for a failing process; `if cmd` did not.
6. **Read the number the script prints.** A final-state listing answers "what is true now", never "what
   happened" — the difference was once an entire false finding.
7. **Transplant onto the newest main; never overwrite a file from an older tree.** That is a merge done as a
   copy, and it silently reverted a rule-fix.
8. **A staleness check that watches half the tree produces the wrong conclusion.** Half the build inputs meant
   a fix looked like it had failed.

## What is now pinned by a test

- the classifier's verdicts on five faults, from real processes (`experiments/rig/verify-matrix.sh`, inside
  `make exp`), asserted rather than merely recorded;
- both directory layouts, in `internal/observe` and `internal/loop`;
- that a receipt cannot claim `verified` without a content check;
- that files a manifest does not name are reported, not judged.

## Still open, and said plainly

**The loop is not in the cluster.** The matrix proves the classifier; `run.sh c1` proves preemption (one holder
taken, exactly as designed). The join — a request that reaches a writer, a write that verifies, under a real
eviction — has run locally in the demo and never under Kueue. Two green halves are not a joined whole, and the
write-up must not imply otherwise.

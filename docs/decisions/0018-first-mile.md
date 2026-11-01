# 0018 — The first mile: three defaults and one sentence

Date: 2026-11-01. Status: accepted. Fills in the defaults that 0017 left to the surface
(`docs/product-v1.md` §13) and closes the last open question on that page.

## Context

0017 settled what the product decides, and deliberately left the surface to a separate page. A
person who has never configured anything never meets a contract: they meet defaults. Four of them
were unwritten, so each was being re-argued every time someone asked "why this number":

1. what `init` recommends when the user says how long the run is;
2. what happens when a run outlives the duration it was asked about;
3. what `init` writes when the answer is "nothing";
4. what the receipt says when no window was ever set.

## Decision

**1. The default recommendation is one write at the middle** — `with-us = D/2`, not a periodic window.
For someone who has never written a checkpoint, one write for half the run is worth more than eight
for thirty minutes, and it is the smallest thing that makes the emergency save meaningful. A window
remains selectable; it is not the thing we put in front of a beginner.

**2. A run that passes its stated duration is not re-quoted.** The tax stays proportional to the run,
so nothing has to change; prompting at hour 8 would be a notification about arithmetic. The duration
is an input to a price, not a promise the run was measured against.

**3. `init` writes nothing when the answer is "skip".** The configuration file stays absent and the
next run says `no-us none (unset)`. Writing `no-us: none` would make a run that nobody configured
look configured, which is the opposite of what the word zero-config is for.

**4. The receipt always states the bound, including when `with-us` is unset.** The last line is not
conditional:

```
run 3s · no writes
  nothing said the work was at risk
  a silent death costs everything: no window was set
```

That sentence is the one thing a person needs to answer "what does losing this machine cost me?", and
it is exactly the case where nobody thought about the question.

## Consequences

- `ckptctl init --duration 4h` prints three priced answers and recommends nothing; the person chooses
  between "everything", "half the run" and "the window". The default is the middle row, not a hidden
  flag.
- Nothing escalates when a run runs long. If the estimate was wrong, the price of the window is still
  proportional, and the receipt reports the writes that actually happened.
- `numbers.json` exists only when somebody answered. A deployment with no file is a deployment with no
  opinion, and the run says so on its first line.
- The receipt has no branch in which the hole is unmentioned. A tool that stays quiet about an unset
  bound has not been silent; it has implied there is no bound.
- Rejected: recommending a periodic window by default (adds writes nobody asked for, and makes the
  first write look like a schedule); re-quoting past the duration (a prompt about arithmetic); writing
  `no-us: none` on skip (a configuration that looks chosen).

## What this does not promise

That the middle row is right for everyone. It is the right default because it is the smallest
protection that covers half a run; anyone with a different risk appetite can pick another row, and the
receipt will tell them what it cost.

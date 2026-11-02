# Governance

## Who decides

One maintainer today: `@bolubo`. The bus factor is one, and this page says so rather than implying a
committee that does not exist.

The maintainer decides what the product claims, what a release contains, and whether a pull request
merges. A change to what the product decides needs a decision record first;
`docs/decisions/README.md` states that rule and `CONTRIBUTING.md` repeats it at the point of work.

## How a decision changes

A decision is a numbered file in `docs/decisions/`. It changes in one of two ways:

- a new record with a new number, and the old one marked `superseded` with a line naming what replaced it;
- the same number, amended, when the change is a correction and not a reversal. The diff is the record.

Nothing is decided in a review comment that is not written down afterwards. If a thread ends with a
conclusion, one of us opens the record.

## How a change arrives

Every change arrives as a pull request against `main`, and `main` is protected by the `verify`
check. Branch, pull request, squash — never a direct push. One pull request per batch:
`CONTRIBUTING.md` explains why a CI run per file is the expensive way to work.

Review is by the code owner of the touched path (`.github/CODEOWNERS`). With one maintainer, review
is a second read, not a second signature; the required check is what actually holds the line.

## Becoming a maintainer

Becoming a maintainer is earned by work that stayed: several merged changes, at least one of them in
`internal/policy` or a contract, plus a review of someone else's change that caught something real.
The maintainer adds the new owner to `CODEOWNERS`, and the change is recorded here with a date.

A maintainer who stops reviewing for a season is not removed by a process; they are asked whether
they still want the role.

## Issues and labels

An issue is tagged when it is read: `kind/*` for what it is, `area/*` for where it lives,
`good first issue` when the work fits one sitting and the issue states a done-when. Anything that
needs a decision record says so in its first comment.

There are no priority numbers. There is one roadmap, `docs/plan-library.md`, and it is ordered.

## Out of scope, by decision

The product does not become a scheduler, a storage layer, a dashboard, or a certification body.
Those refusals are decision 0020 and 0028. Reopening one takes a decision record that answers the
conditions the earlier record named, not a longer thread.

## Changing this page

This page is itself a decision. Change it with a record that names what stops working under the old
one, and date the change at the top of that record.

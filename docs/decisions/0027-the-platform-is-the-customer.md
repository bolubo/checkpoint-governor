# 0027 — The platform is the customer, the job is the beneficiary

Date: 2026-11-02. Status: **superseded by 0028 (the direction is a library, not a platform)**. Extends
0019 (deployment without a custom resource) and 0020 (what this is not). Evidence: `docs/references.md`.
Execution: `docs/plan-platform.md` (also superseded).

The installer/payer analysis below still holds and is worth keeping: the installer, the payer and the
beneficiary are different parties, and a cloud vendor's ledger may point against fewer writes. What 0028
replaces is the sequencing — a library in the training loop first, under a community goal.

## Context

v1 assumed the buyer was the job author: a person who never configured checkpointing and would pay to
keep not thinking about it. The research in `docs/references.md` changes who that person is, without
changing what they want:

- **The job author does not make this decision, and never did.** Where a default exists, it was set by a
  framework: `save_steps=500` in `transformers`, one save per epoch in Lightning, `1ep` and "keep
  everything" in Composer. Where it does not, the number is typed into a launch script.
- **All of those defaults are blind in the same three ways**: they count steps or epochs instead of
  seconds; they do not know a deadline; they do not know what a write costs on this disk. A framework
  cannot fix that with a better constant, because the constant is chosen before the job exists.
- **The two parties that could install a policy are a platform operator and a framework owner.** The
  first owns the incidents, the storage bill and the tenant support queue; the second owns the default.
  Neither pays per job; both pay per install.

So the market is not "many job authors each choosing a CLI". It is **a small number of installs, each
covering many jobs**, and the thing being bought is a guarantee the installer can put in front of its
tenants.

## Decision

1. **The counterparty is the platform or framework owner. The job owner is the beneficiary and
   configures nothing.** Every surface we ship is addressed to the installer: one artefact, one install
   path, one page of terms.
2. **The unit of the sale is a statement, not a feature list.** For each job: an upper bound on what a
   loss can cost, the exclusions that bound depends on, and the price in writes and seconds. The
   certificate (one page per job) is a deliverable, not documentation.
3. **Two delivery shapes, one core.** A sidecar the platform runs beside every job (no job change), and a
   library a framework embeds at its save point (three lines, the `contrib` shape we already ship). Both
   speak the same plan-file contract; 0019 stands, and no CRD is introduced.
4. **We replace the decision, not the number.** The only defensible reason a platform adopts a component
   instead of tuning a constant is that the component measures what the constant cannot: how long a write
   takes here, how much time is left, how fast the job is stepping. A static default cannot read any of
   those.
5. **Distribution order is platform → framework → managed service.** A framework default is only won the
   way safetensors won it — by being a dependency that removes a risk at zero cost — so it is last, and
   only after the platform proof exists. A managed, audited service is later still.
6. **Evidence before the pitch.** No integration work until the replacement experiment prints its three
   numbers (writes, IO seconds, loss bound). If a policy cannot cut writes at an equal bound, the cost
   argument is gone and this record has to be revisited.
7. **The refusals stay.** No scheduler, no checkpoint bytes, no dashboard, no conformance kit, no adapter
   zoo (0020). A platform integration is an install path and a contract, not a new product.

## Consequences

- **The first screen changes its reader.** README and `docs/install.md` answer "how does an operator
  install this once for all jobs", not "how do I try this on one job". The one-job path stays, as the
  way an operator verifies the install.
- **The 1% become the second sale, priced.** Teams that already configure well buy fewer writes at the
  same bound; that is a measurable number, not a claim.
- **What we must measure and publish**: writes per hour, IO seconds per hour, loss bound, drills run.
  A platform will ask for these before it asks for features.
- **How this fails**: if writes cannot be cut at an equal bound, or if the bound cannot be stated
  honestly for a job whose pre-emption rate is unknown, the insurance framing loses its arithmetic. The
  fallback is a narrower claim ("we catch the last-minute loss the platform cannot"), which is a weaker
  product but still a platform sale.

# Documentation index

One page per question. Start where your question is. Every local link here resolves —
`TestDocsHaveNoDeadLinks` fails the build when one does not.

## If you are installing it

| page | question |
|---|---|
| [install.md](install.md) | how do I get the binaries, the image, or the Python package? |
| [install-python.md](install-python.md) | `pip install`, three lines in a loop, and what to do when it fails |
| [compatibility.md](compatibility.md) | what does a version promise to keep? |

## If you are wiring it into a training loop

| page | question |
|---|---|
| [integration-template.md](integration-template.md) | how do I add a framework? the six rules, the files, the test |
| [huggingface.md](huggingface.md) | Trainer: `save_strategy="no"`, one callback, the `logging_steps` latency |
| [lightning.md](lightning.md) | Lightning: one callback, one batch of latency |
| [composer.md](composer.md) | Composer: the callback, and what `CheckpointSaver` used to do |
| [writer-contract.md](writer-contract.md) | what must a checkpoint writer do so that a restore works? |

## If you want to know what it decides, and why

| page | question |
|---|---|
| [policy.md](policy.md) | how is a write decision computed? |
| [conditions.md](conditions.md) | the condition names, one card each, and who produces them |
| [verify-and-demand.md](verify-and-demand.md) | what happens after a write lands? |
| [resume-contract.md](resume-contract.md) | what does "this can continue" mean, and what proves it? |
| [glue.md](glue.md) | what the job writes into the directory, and what reads it |
| [decisions/README.md](decisions/README.md) | why is it this way? every record that is still load-bearing |

## If you are asking what it costs

| page | question |
|---|---|
| [waste.md](waste.md) | what do the framework defaults cost, at four scales? |
| [references.md](references.md) | what do the neighbouring projects actually do? |
| [retention-budget.md](retention-budget.md) | why does the volume fill up, and how does it stop? |
| [dev-footprint.md](dev-footprint.md) | what do the experiments cost on a development machine? |
| [experiments-matrix.md](experiments-matrix.md) | which experiment answers which claim? |

## If you are operating it

| page | question |
|---|---|
| [design.md](design.md) | how is the backstop built? |
| [ci.md](ci.md) | what runs on a push, and why that and nothing more? |
| [operator-notes.md](operator-notes.md) | what must the platform allow: the grace a checkpoint needs |
| [first-edition-parity.md](first-edition-parity.md) | what did the first edition have that this one does? |

## If you are following the work

The current number is **`v0.21.0`** (decision 0029). `v0.15.0` and `v0.2.0` are two earlier numbering
schemes; their pages are history, not install instructions.

| page | question |
|---|---|
| [release-0.21.md](release-0.21.md) | **the current release**: where the loop closed, under a real eviction |
| [plan-library.md](plan-library.md) | the roadmap: what is done, what is next, and the command that proves each line |
| [product-v1.md](product-v1.md) | v1: the surface |
| [product-v0.15.md](product-v0.15.md) | history: the release that could be installed, and proved |
| [product-v0.2.md](product-v0.2.md) | history: the release that says whether the work can continue |
| [plan-platform.md](plan-platform.md) | superseded by the library direction; kept as history |

## Outside this folder

| page | question |
|---|---|
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | how to send a change: conventions, one pull request, the gate |
| [../GOVERNANCE.md](../GOVERNANCE.md) | who decides, and how a decision is replaced |
| [../CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md) | how people are expected to behave |
| [../SECURITY.md](../SECURITY.md) | how to report a vulnerability, privately |
| [../RELEASE.md](../RELEASE.md) | how a tag becomes a release |

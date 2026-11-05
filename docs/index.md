# Documentation index

One page per question. Start where your question is. Every local link here resolves —
`TestDocsHaveNoDeadLinks` fails the build when one does not.

## If you are installing it

| page | question |
|---|---|
| [install.md](install.md) | how do I get the binaries, the image, or the Python package? |
| [install-python.md](install-python.md) | `pip install`, three lines in a loop, and what to do when it fails |
| [first-ten-minutes.md](first-ten-minutes.md) | one real run: the commands, the lines they print, and where the files are |
| [compatibility.md](compatibility.md) | what does a version promise to keep? |

## If you are wiring it into a training loop

| page | question |
|---|---|
| [integration-template.md](integration-template.md) | how do I add a framework? the six rules, the files, the test |
| [huggingface.md](huggingface.md) | Trainer: the saver stays, only its alarm moves; one callback, the `logging_steps` latency |
| [lightning.md](lightning.md) | Lightning: one callback, one batch of latency |
| [composer.md](composer.md) | Composer: the callback, and what `CheckpointSaver` used to do |
| [multi-rank.md](multi-rank.md) | several ranks: who starts the engine, where the shards go, who seals |
| [writer-contract.md](writer-contract.md) | what must a checkpoint writer do so that a restore works? |

## If you want to know what it decides, and why

| page | question |
|---|---|
| [policy.md](policy.md) | how is a write decision computed? |
| [conditions.md](conditions.md) | the condition names, one card each, and who produces them |
| [verify-and-demand.md](verify-and-demand.md) | what happens after a write lands? |
| [resume-contract.md](resume-contract.md) | what does "this can continue" mean, for any writer's layout, and what proves it? |
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
| [design.md](design.md) | how is it built? |
| [ci.md](ci.md) | what runs on a push, and why that and nothing more? |
| [operator-notes.md](operator-notes.md) | what must the platform allow: the grace a checkpoint needs |
| [contrast.md](contrast.md) | what does the four-policy comparison actually say, and how do I reproduce it? |
| [gate.md](gate.md) | how do I give a higher-priority arrival lead time on a cluster? |

## If something went wrong

| page | question |
|---|---|
| [troubleshooting.md](troubleshooting.md) | what does this message mean, and what do I do? |

## If you are following the work

The current number is **`v0.26.0`** (decision 0029). `v0.25.0`, `v0.24.0`, `v0.23.0`, `v0.22.0`,
`v0.21.0`, `v0.15.0` and `v0.2.0` are earlier numbers; their pages are history, not install instructions.

| page | question |
|---|---|
| [release-0.26.md](release-0.26.md) | **the current release**: the command, the check, and the glue |
| [release-0.25.md](release-0.25.md) | history: installed, run for real, said exactly |
| [release-0.24.md](release-0.24.md) | history: the copy can be judged, whoever wrote it |
| [release-0.23.md](release-0.23.md) | history: the hold covers the nodes the arrival needs |
| [release-0.22.md](release-0.22.md) | history: the arrival waits for the write |
| [release-0.21.md](release-0.21.md) | history: where the loop closed, under a real eviction |
| [plan-library.md](plan-library.md) | the roadmap: what is done, what is next, and the command that proves each line |
| [product-v1.md](product-v1.md) | design: the surface a user meets |
| [product-v0.15.md](product-v0.15.md) | history: the release that could be installed, and proved |
| [product-v0.2.md](product-v0.2.md) | history: the release that says whether the work can continue |
| [plan-platform.md](plan-platform.md) | superseded by the library direction; kept as history |

## Outside this folder

| page | question |
|---|---|
| [CONTRIBUTING.md](https://github.com/bolubo/checkpoint-governor/blob/main/CONTRIBUTING.md) | how to send a change: conventions, one pull request, the gate |
| [GOVERNANCE.md](https://github.com/bolubo/checkpoint-governor/blob/main/GOVERNANCE.md) | who decides, and how a decision is replaced |
| [CODE_OF_CONDUCT.md](https://github.com/bolubo/checkpoint-governor/blob/main/CODE_OF_CONDUCT.md) | how people are expected to behave |
| [SECURITY.md](https://github.com/bolubo/checkpoint-governor/blob/main/SECURITY.md) | how to report a vulnerability, privately |
| [RELEASE.md](https://github.com/bolubo/checkpoint-governor/blob/main/RELEASE.md) | how a tag becomes a release |
| [Discussions](https://github.com/bolubo/checkpoint-governor/discussions) | questions, ideas, and what people built with it |
| [good first issues](https://github.com/bolubo/checkpoint-governor/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22) | where a first contribution starts |

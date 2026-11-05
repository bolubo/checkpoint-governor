# Recipes

One page per way of wiring the governor in. Each recipe is a copy block and the line it should print,
not a tutorial: the reasoning is in the page it links to.

| recipe | what it is |
|---|---|
| [hf.md](hf.md) | Hugging Face Trainer: the callback and the four fields that move the alarm |
| [custom-writer.md](custom-writer.md) | a writer we have never heard of: `layout.json` and a probe command |

The Lightning and Composer callbacks are supported and tested (`contrib/ckpt-policy/tests/`); the
copyable example loops are still open (#202 and #203). This release does not ship a runnable recipe
for them rather than shipping one nobody ran.

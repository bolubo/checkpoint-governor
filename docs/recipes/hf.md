# Hugging Face Trainer, one file

```bash
python3 contrib/ckpt-policy/examples/hf_trainer.py /tmp/ckpt-demo 200
```

The example is the whole recipe: a `TinyModel`, a `Trainer`, and the marked block.

```python
from ckpt_policy import Governor
from ckpt_policy.hf import GovernorCallback

gov = Governor("/tmp/ckpt-demo", start_engine=True)
trainer = Trainer(
    model=model, args=args, train_dataset=ds, eval_dataset=ds,
    callbacks=[GovernorCallback(gov)],
)
trainer.train()
gov.ending()
gov.close()
```

The callback asks the policy at every logging boundary; the framework's own periodic save is turned
off by the four `TrainingArguments` fields the example sets. It writes nothing while training unless
a condition or the window asks for a write.

Expected lines (CPU or one GPU):

```
trained 200 steps in 4.5s
checkpoints: 1
resumable (step 200, requires model, optimizer, scheduler, rng, caveat: the data position is not saved; a restore replays or restarts the epoch)
```

Then:

```bash
ckptctl status /tmp/ckpt-demo
ckptctl evidence --dir=/tmp/ckpt-demo --json
```

The caveat is part of the answer: the Trainer re-iterates the dataset, so there is no data position
to restore. That does not make the copy unusable; it makes the promise smaller, and the line says so.

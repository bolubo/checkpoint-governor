"""Tests for the adapter, with the standard library only: the package has no runtime dependencies, so the
suite must not need any either (a test that installs torch is a test nobody runs on a runner).
"""

import hashlib
import json
import os
import sys
import tempfile
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from ckpt_policy import Governor, Saves, World  # noqa: E402
from ckpt_policy.glue import BASE_DIR  # noqa: E402
from ckpt_policy.governor import engine_path  # noqa: E402


def plan(root, **fields):
    """Write a plan the way the controller does, so the adapter reads the real shape."""
    doc = {"schema": 2, "epoch": 1, "expiresAt": int(time.time()) + 30, "unitId": "job-1",
           "generation": 7, "action": "save", "reason": "risk.preempt", "requestId": "r-7",
           "baselineSeconds": 0, "baselineNone": True, "windowSeconds": 600}
    doc.update(fields)
    os.makedirs(os.path.join(root, BASE_DIR), exist_ok=True)
    with open(os.path.join(root, BASE_DIR, "state.json"), "w", encoding="utf-8") as f:
        json.dump(doc, f)


def glue(root, name):
    with open(os.path.join(root, BASE_DIR, name), "r", encoding="utf-8") as f:
        return f.read().strip()


class TestRequested(unittest.TestCase):
    def test_reads_the_generation_the_plan_asks_for(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, world=World(ranks=8))
            plan(root, generation=7)
            self.assertEqual(g.requested(), 7)

    def test_a_plan_from_another_schema_is_not_a_request(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, schema=1, generation=7)
            self.assertEqual(g.requested(), 0)

    def test_an_expired_plan_is_not_a_request(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, expiresAt=int(time.time()) - 1)
            self.assertEqual(g.requested(), 0)

    def test_a_request_is_acted_on_at_most_once(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            self.assertEqual(g.requested(), 7)
            gen_dir = os.path.join(root, "gen-0007")
            os.makedirs(gen_dir)
            with open(os.path.join(gen_dir, "rank-00.bin"), "wb") as f:
                f.write(b"weights")
            g.sealed(7, 512, ["rank-00.bin"])
            self.assertEqual(g.requested(), 0, "the same request was asked for twice")


class TestSealed(unittest.TestCase):
    def test_the_manifest_describes_the_bytes_that_are_there(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, world=World(ranks=2, reshardable=False),
                         saves=Saves(model=True, optimizer=True, scheduler=True, rng=True,
                                     dataloader=True))
            gen_dir = os.path.join(root, "gen-0007")
            os.makedirs(gen_dir)
            payloads = {f"rank-{i:02d}.bin": bytes([i]) * 64 for i in range(2)}
            for name, data in payloads.items():
                with open(os.path.join(gen_dir, name), "wb") as f:
                    f.write(data)

            m = g.sealed(7, 512, list(payloads))
            self.assertEqual(m["generation"], 7)
            self.assertEqual(m["step"], 512)
            self.assertEqual(m["writer"], "ckpt-policy")
            self.assertEqual([s["name"] for s in m["shards"]], ["rank-00.bin", "rank-01.bin"])
            self.assertEqual([s["size"] for s in m["shards"]], [64, 64])
            # The hash is of the file that is on disk, not of what the caller said it wrote.
            for entry in m["shards"]:
                with open(os.path.join(gen_dir, entry["name"]), "rb") as f:
                    self.assertEqual(hashlib.sha256(f.read()).hexdigest(), entry["sha256"])

            with open(os.path.join(gen_dir, "resume.json"), encoding="utf-8") as f:
                decl = json.load(f)
            self.assertEqual(decl["schema"], 1)
            self.assertEqual(decl["generation"], 7)
            self.assertEqual(decl["world"]["ranks"], 2)
            self.assertTrue(decl["state"]["optimizer"])
            self.assertFalse(decl["state"]["ema"])
            self.assertNotIn("missing", decl, "the missing list is our conclusion, not the writer's claim")

    def test_a_weights_only_save_declares_weights_only(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, saves=Saves(model=True, optimizer=False, scheduler=False,
                                           rng=False, dataloader=False))
            gen_dir = os.path.join(root, "gen-0001")
            os.makedirs(gen_dir)
            with open(os.path.join(gen_dir, "rank-00.bin"), "wb") as f:
                f.write(b"w")
            g.sealed(1, 10, ["rank-00.bin"])
            with open(os.path.join(gen_dir, "resume.json"), encoding="utf-8") as f:
                decl = json.load(f)
            self.assertFalse(decl["state"]["optimizer"])


class TestResumedAndGlue(unittest.TestCase):
    def test_resumed_reports_what_the_loader_adopted(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.resumed(7, 512, seconds=9.1)
            self.assertEqual(glue(root, "resumed-generation"), "7")
            self.assertEqual(glue(root, "resumed-step"), "512")
            self.assertEqual(glue(root, "load-seconds"), "9.100")

    def test_a_step_is_one_small_file(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.step(512)
            self.assertEqual(glue(root, "step"), "512")

    def test_sigterm_records_the_deadline_the_job_was_given(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.on_sigterm(grace_seconds=20)
            self.assertEqual(glue(root, "stopping"), "1")
            self.assertEqual(glue(root, "seconds-left"), "20")

    def test_ending_says_the_work_that_exists_now_is_the_work_that_matters(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.ending()
            self.assertEqual(glue(root, "ending"), "1")

    def test_only_rank0_writes(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, rank0=False, world=World(ranks=8))
            g.step(3)
            self.assertFalse(os.path.exists(os.path.join(root, BASE_DIR, "step")))


class TestTheCallbackWritesOnlyGlue(unittest.TestCase):
    """The whole point of 0025: the bytes belong to the library. This test is the mechanical proof."""

    ALLOWED = {"boot", "members", "expected-bytes", "step-baseline", "step", "step-rate",
               "seconds-left", "stopping", "ending", "resumed-generation", "resumed-step",
               "load-seconds", "state.json"}
    ALLOWED_IN_GENERATION = {"manifest.json", "resume.json"}

    def test_one_cycle_creates_no_shard(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, expected_bytes=2048)
            plan(root, generation=7)
            g.step(1)
            gen_dir = os.path.join(root, "gen-0007")
            os.makedirs(gen_dir)
            # The user's save function writes the shards; the test stands in for it.
            for name in ("rank-00.bin", "rank-01.bin"):
                with open(os.path.join(gen_dir, name), "wb") as f:
                    f.write(b"\x00" * 1024)
            before = set(os.listdir(gen_dir))
            g.sealed(7, 512, ["rank-00.bin", "rank-01.bin"])
            g.resumed(7, 491, seconds=0.2)

            for name in os.listdir(os.path.join(root, BASE_DIR)):
                self.assertIn(name, self.ALLOWED, f"the callback wrote {name}, which is not glue")
            added = set(os.listdir(gen_dir)) - before
            for name in added:
                self.assertIn(name, self.ALLOWED_IN_GENERATION,
                              f"the callback wrote {name}, which is not metadata")

    def test_a_write_leaves_no_temporary_names(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.step(1)
            names = os.listdir(os.path.join(root, BASE_DIR))
            self.assertEqual([n for n in names if n.startswith(".")], [],
                             "a half-written file was left visible")


class TestShouldSave(unittest.TestCase):
    """`should_save` is the whole write side: the plan says save, or it does not."""

    def test_follows_the_plan(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            self.assertTrue(g.should_save())
            plan(root, action="skip", generation=0)
            self.assertFalse(g.should_save())
            plan(root, expiresAt=int(time.time()) - 1)
            self.assertFalse(g.should_save(), "an expired plan is not a request")


class TestEngine(unittest.TestCase):
    """The wheel carries the engine; the client starts it and reads the plan it writes."""

    def test_starting_the_engine_makes_a_plan_and_stays_quiet(self):
        if engine_path() is None:
            self.skipTest("no ckptctl engine: build it with hack/build-wheel.sh, or set CKPTCTL_ENGINE")
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, start_engine=True, engine_every="200ms")
            try:
                state = os.path.join(root, BASE_DIR, "state.json")
                deadline = time.time() + 5
                while time.time() < deadline and not os.path.exists(state):
                    time.sleep(0.05)
                self.assertTrue(os.path.exists(state), "the engine started but wrote no plan")
                self.assertTrue(g.engine_running())
                self.assertFalse(g.should_save(), "nothing is at risk, so the plan asks for nothing")
            finally:
                g.close()
            self.assertFalse(g.engine_running())

    def test_a_dead_engine_degrades_quietly(self):
        if engine_path() is None:
            self.skipTest("no ckptctl engine: build it with hack/build-wheel.sh, or set CKPTCTL_ENGINE")
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, start_engine=True, engine_every="200ms")
            try:
                state = os.path.join(root, BASE_DIR, "state.json")
                deadline = time.time() + 5
                while time.time() < deadline and not os.path.exists(state):
                    time.sleep(0.05)
                self.assertTrue(os.path.exists(state))
                g._engine.kill()
                g._engine.wait()
                self.assertFalse(g.engine_running())
                # The plan ages out; the client reports "no write" instead of raising. The caller's
                # fallback is the end-of-training write and the announced stop.
                self.assertFalse(g.should_save())
            finally:
                g.close()

    def test_close_is_idempotent(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.close()
            g.close()
            self.assertFalse(g.engine_running())

    def test_a_missing_engine_says_what_to_do(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, start_engine=False)
            g._engine_bin = "/nonexistent/ckptctl"
            with self.assertRaises(RuntimeError) as caught:
                g.start_engine()
            self.assertIn("CKPTCTL_ENGINE", str(caught.exception))


class TestDemo(unittest.TestCase):
    """The demo is the library's own proof: a cadence against the policy, on one synthetic job."""

    def test_the_two_arms_report_their_counts(self):
        if engine_path() is None:
            self.skipTest("no ckptctl engine: build it with hack/build-wheel.sh, or set CKPTCTL_ENGINE")
        from ckpt_policy.demo import run

        rows = run(steps=1000, save_every=500, write_ms=20)
        self.assertEqual([r.writes for r in rows], [2, 1],
                         "the cadence writes twice; the policy writes only at the end")
        self.assertEqual(rows[1].note, "", "the engine should have published a plan")


class _Control:
    def __init__(self):
        self.should_save = False


class _State:
    global_step = 0


class _FakeTrainer:
    """The two Trainer behaviours that matter: its cadence, and `should_save` consumed at log steps.

    HF has no torch on the runner, so the test drives this instead of transformers. The contract is the
    same one the callback relies on: a request is honoured at the next logging step, and only once,
    because the generation it served is recorded by `sealed`.
    """

    def __init__(self, save_every=None, logging_steps=50):
        self.save_every, self.logging_steps = save_every, logging_steps
        self.control, self.state = _Control(), _State()
        self.saves = []

    def train(self, steps, callbacks=(), governor=None):
        for step in range(1, steps + 1):
            self.state.global_step = step
            self.control.should_save = bool(self.save_every) and step % self.save_every == 0
            for cb in callbacks:
                cb.on_step_end(None, self.state, self.control)
            if step % self.logging_steps == 0 and self.control.should_save:
                self.saves.append(step)
                self.control.should_save = False
                if governor is not None:
                    governor.sealed(governor.generation(), step, [])
        for cb in callbacks:
            cb.on_train_end(None, self.state, self.control)


class TestHuggingFaceCallback(unittest.TestCase):
    def test_the_framework_default_writes_twice(self):
        trainer = _FakeTrainer(save_every=500, logging_steps=50)
        trainer.train(1000)
        self.assertEqual(trainer.saves, [500, 1000])

    def test_the_callback_writes_nothing_when_the_plan_is_silent(self):
        from ckpt_policy.hf import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            cb = GovernorCallback(g)
            trainer = _FakeTrainer(logging_steps=10)
            trainer.train(1000, callbacks=[cb], governor=g)
            self.assertEqual(trainer.saves, [], "nothing is at risk, so nothing is written")
            self.assertEqual(cb.requests, 0)
            self.assertEqual(glue(root, "ending"), "1", "the callback records the end of training")

    def test_a_request_is_honoured_at_the_next_logging_step_and_once(self):
        from ckpt_policy.hf import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            cb = GovernorCallback(g)
            trainer = _FakeTrainer(logging_steps=10)
            trainer.train(100, callbacks=[cb], governor=g)
            self.assertEqual(trainer.saves, [10],
                             "the request waits for the next logging step, then is written once")
            self.assertFalse(g.should_save(), "the served generation is not asked for twice")

    def test_the_callback_returns_the_control_it_was_given(self):
        from ckpt_policy.hf import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            cb = GovernorCallback(g)
            control = _Control()
            self.assertIs(cb.on_step_end(None, _State(), control), control)
            self.assertIs(cb.on_train_end(None, _State(), control), control)


class _FakeLightningTrainer:
    def __init__(self, root):
        self.default_root_dir = root
        self.global_step = 0
        self.saved = []

    def save_checkpoint(self, path):
        self.saved.append(path)
        with open(path, "w", encoding="utf-8") as f:
            f.write("ckpt")


class TestLightningCallback(unittest.TestCase):
    def test_nothing_is_written_while_the_plan_is_silent(self):
        from ckpt_policy.lightning import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            trainer = _FakeLightningTrainer(root)
            cb = GovernorCallback(g)
            for step in range(1, 101):
                trainer.global_step = step
                cb.on_train_batch_end(trainer, None)
            self.assertEqual(trainer.saved, [])
            self.assertEqual(cb.saves, 0)

    def test_a_request_is_honoured_at_the_next_batch_end_and_once(self):
        from ckpt_policy.lightning import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            trainer = _FakeLightningTrainer(root)
            cb = GovernorCallback(g)
            for step in range(1, 101):
                trainer.global_step = step
                cb.on_train_batch_end(trainer, None)
            self.assertEqual(len(trainer.saved), 1, "one request, one checkpoint")
            self.assertTrue(trainer.saved[0].endswith("ckpt-step-1.ckpt"))
            self.assertFalse(g.should_save(), "the served generation is not asked for twice")

    def test_a_custom_filename_is_used(self):
        from ckpt_policy.lightning import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=3)
            trainer = _FakeLightningTrainer(root)
            cb = GovernorCallback(g, filename=lambda t: os.path.join(t.default_root_dir, "custom.ckpt"))
            trainer.global_step = 1
            cb.on_train_batch_end(trainer, None)
            self.assertEqual(trainer.saved[-1], os.path.join(root, "custom.ckpt"))

    def test_the_end_of_training_is_recorded(self):
        from ckpt_policy.lightning import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            cb = GovernorCallback(g)
            cb.on_train_end(_FakeLightningTrainer(root), None)
            self.assertEqual(glue(root, "ending"), "1")


class TestServed(unittest.TestCase):
    """A framework that writes its own format still has to mark the request as served."""

    def test_served_marks_without_writing_metadata(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=9)
            self.assertTrue(g.should_save())
            g.served(9)
            self.assertFalse(g.should_save())
            gens = [n for n in os.listdir(root) if n.startswith("gen-")]
            self.assertEqual(gens, [], "served() records the request; it does not write a checkpoint")


if __name__ == "__main__":
    unittest.main()

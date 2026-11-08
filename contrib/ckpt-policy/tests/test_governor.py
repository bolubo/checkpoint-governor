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
from unittest.mock import patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from ckpt_policy import Governor, Saves, World  # noqa: E402
from ckpt_policy.glue import BASE_DIR, write_text  # noqa: E402
from ckpt_policy.governor import engine_path  # noqa: E402


class TestDemoJson(unittest.TestCase):
    def test_json_output_contains_both_rows_and_required_fields(self):
        from contextlib import redirect_stdout
        from io import StringIO
        from types import SimpleNamespace
        from ckpt_policy import demo

        rows = [
            SimpleNamespace(policy="framework default", writes=2, io=0.2, overhead=0.0, note=""),
            SimpleNamespace(policy="ckpt-policy, unconfigured", writes=1, io=0.1, overhead=0.03,
                            note="no plan: the engine did not start"),
        ]
        output = StringIO()
        with patch.object(demo, "engine_path", return_value="/engine"), \
                patch.object(demo, "run", return_value=rows), \
                redirect_stdout(output):
            self.assertEqual(demo.main(["--json"]), 0)

        result = json.loads(output.getvalue())
        self.assertEqual(len(result), 2)
        required_fields = {"policy", "writes", "io", "overhead", "note"}
        self.assertTrue(all(required_fields <= row.keys() for row in result))
        self.assertEqual([row["writes"] for row in result], [2, 1])
        self.assertEqual([row["note"] for row in result],
                         ["", "no plan: the engine did not start"])


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


class TestTorchrunEnv(unittest.TestCase):
    """A `torchrun` job already says who each process is; the client should not ask for it twice."""

    def test_rank_and_world_come_from_the_launcher(self):
        with tempfile.TemporaryDirectory() as root, \
                patch.dict(os.environ, {"RANK": "1", "WORLD_SIZE": "2"}):
            g = Governor(root, start_engine=True)
            try:
                self.assertEqual((g.rank, g.rank0, g.world.ranks), (1, False, 2))
                self.assertFalse(g.engine_running())
                self.assertFalse(os.path.exists(os.path.join(root, BASE_DIR, "members")))
            finally:
                g.close()

    def test_rank_zero_keeps_the_lead(self):
        with tempfile.TemporaryDirectory() as root, \
                patch.dict(os.environ, {"RANK": "0", "WORLD_SIZE": "2"}):
            g = Governor(root)
            try:
                self.assertEqual((g.rank, g.rank0, g.world.ranks), (0, True, 2))
            finally:
                g.close()

    def test_an_explicit_rank_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as root, \
                patch.dict(os.environ, {"RANK": "1", "WORLD_SIZE": "4"}):
            g = Governor(root, rank=2, rank0=False)
            try:
                self.assertEqual((g.rank, g.world.ranks), (2, 4))
            finally:
                g.close()


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

    def test_a_first_request_names_no_generation_and_is_acted_on(self):
        """A plan for a first checkpoint says generation 0: there is no copy yet to number."""
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=0, requestId="r-first")
            self.assertTrue(g.should_save(), "generation 0 is a request, not a refusal")
            self.assertEqual(g.generation(), 1, "the client numbers the first copy")
            gen_dir = os.path.join(root, "gen-0001")
            os.makedirs(gen_dir)
            with open(os.path.join(gen_dir, "rank-00.bin"), "wb") as f:
                f.write(b"weights")
            g.sealed(1, 512, ["rank-00.bin"])
            self.assertFalse(g.should_save(), "the same request was asked for twice")

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

    def test_a_shard_that_is_not_readable_is_not_sealed(self):
        """The manifest is a promise about the disk, so the disk is asked before the promise is made."""
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, world=World(ranks=2))
            gen_dir = os.path.join(root, "gen-0007")
            os.makedirs(gen_dir)
            with open(os.path.join(gen_dir, "rank-00.bin"), "wb") as f:
                f.write(b"\x00" * 64)
            # rank 1 never landed: the writer's shard is missing.
            result = g.sealed(7, 512, ["rank-00.bin", "rank-01.bin"])
            self.assertEqual(result, {}, "a copy with a missing shard must not be sealed")
            self.assertFalse(os.path.exists(os.path.join(gen_dir, "manifest.json")),
                             "a failed readback must not leave a manifest")
            self.assertFalse(os.path.exists(os.path.join(gen_dir, "resume.json")),
                             "a declaration without a manifest is a promise nobody can check")
            with open(os.path.join(root, BASE_DIR, "seal-failed"), encoding="utf-8") as f:
                failed = json.load(f)
            self.assertEqual(failed["generation"], 7)
            self.assertEqual(len(failed["attempts"]), 2, "a failure is retried once")
            self.assertIn("rank-01.bin", " ".join(failed["attempts"][-1]["problems"]),
                          "the record names the shard")

    def test_a_transient_readback_failure_is_retried(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            gen_dir = os.path.join(root, "gen-0001")
            os.makedirs(gen_dir)
            with open(os.path.join(gen_dir, "rank-00.bin"), "wb") as f:
                f.write(b"w")
            with patch("ckpt_policy.governor.readback_problems",
                       side_effect=[["shard rank-00.bin is unreadable: busy"], []]) as rb:
                m = g.sealed(1, 10, ["rank-00.bin"])
            self.assertEqual(rb.call_count, 2, "one retry is what the contract promises")
            self.assertEqual(m["generation"], 1)
            self.assertTrue(os.path.exists(os.path.join(gen_dir, "manifest.json")))

    def test_a_tail_that_no_longer_matches_is_not_sealed(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            gen_dir = os.path.join(root, "gen-0001")
            os.makedirs(gen_dir)
            path = os.path.join(gen_dir, "rank-00.bin")
            with open(path, "wb") as f:
                f.write(b"A" * 64)
            with patch("ckpt_policy.governor.build_manifest") as build:
                built = {"generation": 1, "step": 10, "shards": [
                    {"name": "rank-00.bin", "size": 64, "sha256": "0" * 64,
                     "tailSha256": "0" * 64}]}
                build.return_value = built
                result = g.sealed(1, 10, ["rank-00.bin"])
            self.assertEqual(result, {})
            self.assertFalse(os.path.exists(os.path.join(gen_dir, "manifest.json")))


class TestTheStepCellOnASharedVolume(unittest.TestCase):
    """A network volume charges every rank for every metadata operation; liveness is read at 1s."""

    class Clock:
        def __init__(self):
            self.t = 0.0

        def __call__(self):
            self.t += 0.001
            return self.t

    def test_shared_writes_at_most_one_step_per_interval(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, storage="shared", step_write_seconds=1.0)
            clock = self.Clock()
            with patch("ckpt_policy.governor.time.monotonic", clock), \
                    patch("ckpt_policy.governor.write_text", wraps=write_text) as wt:
                for step in range(1, 301):
                    g.step(step)
            written = [c for c in wt.call_args_list if c.args[1].endswith("step")]
            self.assertLessEqual(len(written), 3,
                                 "300 steps inside one interval must not write 300 times "
                                 f"(wrote {len(written)})")
            self.assertEqual(glue(root, "step"), "1", "the first step is always written")

    def test_the_held_step_is_flushed_when_the_run_ends(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, storage="shared", step_write_seconds=1.0)
            clock = self.Clock()
            with patch("ckpt_policy.governor.time.monotonic", clock):
                for step in range(1, 301):
                    g.step(step)
                g.ending()
            self.assertEqual(glue(root, "step"), "300",
                             "a run that ends inside the interval still leaves its last step")

    def test_local_keeps_every_step(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            for step in range(1, 6):
                g.step(step)
            self.assertEqual(glue(root, "step"), "5")
            self.assertEqual(glue(root, os.path.join("ranks", "0", "step")), "5")


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

    def test_a_step_writes_one_cell_and_the_leader_also_writes_the_team_cell(self):
        steps = 5
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            real_fsync = os.fsync
            with patch("ckpt_policy.glue.os.fsync", wraps=real_fsync) as fsync:
                for step in range(1, steps + 1):
                    g.step(step)
            self.assertEqual(fsync.call_count, 0,
                             "progress cells rename without a fsync; they are liveness, not data")
            self.assertEqual(glue(root, "step"), str(steps))
            self.assertEqual(glue(root, os.path.join("ranks", "0", "step")), str(steps))

    def test_a_non_leader_writes_only_its_own_cell(self):
        steps = 5
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, rank=1)
            real_fsync = os.fsync
            with patch("ckpt_policy.glue.os.fsync", wraps=real_fsync) as fsync:
                for step in range(1, steps + 1):
                    g.step(step)
            self.assertEqual(fsync.call_count, 0, "a progress cell is never fsynced")
            self.assertEqual(glue(root, os.path.join("ranks", "1", "step")), str(steps))

    def test_the_end_marker_is_still_durable(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            real_fsync = os.fsync
            with patch("ckpt_policy.glue.os.fsync", wraps=real_fsync) as fsync:
                g.ending()
            self.assertEqual(fsync.call_count, 1, "metadata keeps the fsync; only step cells skip it")
            self.assertEqual(glue(root, "ending"), "1")

    def test_step_glue_cost_at_100k_steps(self):
        """Measure the real atomic file write: flush, rename, no fsync for progress cells."""
        steps = 100_000
        # Use tmpfs where available to measure the client/syscall glue without making the result
        # depend on the host's backing disk. Fall back to the platform's normal temp directory.
        benchmark_dir = "/dev/shm" if os.path.isdir("/dev/shm") else None
        with tempfile.TemporaryDirectory(dir=benchmark_dir) as root:
            g = Governor(root)
            start = time.perf_counter_ns()
            for step in range(1, steps + 1):
                g.step(step)
            elapsed_ns = time.perf_counter_ns() - start

            micros_per_step = elapsed_ns / steps / 1_000
            share_of_one_second_step = micros_per_step / 1_000_000 * 100
            print(
                f"glue: {micros_per_step:.1f}µs/step at 100k steps "
                f"({share_of_one_second_step:.4f}% of a 1s training step)",
                flush=True,
            )
            self.assertEqual(glue(root, "step"), str(steps))

    def test_sigterm_records_the_deadline_the_job_was_given(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.on_sigterm(grace_seconds=20)
            self.assertEqual(glue(root, "stopping"), "1")
            self.assertEqual(glue(root, "seconds-left"), "20")

    def test_sigterm_second_call_never_extends_deadline(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            with patch("time.monotonic", side_effect=[100.0, 101.2, 101.3, 101.5]):
                g.on_sigterm(grace_seconds=20)
                self.assertEqual(glue(root, "seconds-left"), "20")
                g.step(1)
                self.assertEqual(glue(root, "seconds-left"), "18")
                # A second call must never restart or extend the deadline
                g.on_sigterm(grace_seconds=20)
                self.assertEqual(glue(root, "seconds-left"), "18")

    def test_sigterm_countdown_under_shared_storage_not_throttled(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, storage="shared", step_write_seconds=1.0)
            with patch("time.monotonic", side_effect=[100.0, 100.0, 100.0, 100.9, 100.9]):
                g.step(0)
                g.on_sigterm(grace_seconds=20)
                self.assertEqual(glue(root, "seconds-left"), "20")
                g.step(1)
                # Step 1 was throttled (< 1.0s), but seconds-left is updated to 19 (<= grace - elapsed)
                self.assertEqual(glue(root, "step"), "0")
                self.assertEqual(glue(root, "seconds-left"), "19")

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

    ALLOWED = {"boot", "members", "ranks", "expected-bytes", "step-baseline", "step", "step-rate",
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


class _FakeSaver:
    """Stands in for a configured ModelCheckpoint: top-k and the latest pointer are its business."""

    def __init__(self, private=True):
        self.calls = 0
        self.topk_calls = 0
        self.last_calls = 0
        self._last_checkpoint_saved = ""
        if private:
            self._save_topk_checkpoint = self._topk
            self._save_last_checkpoint = self._last
            self._monitor_candidates = lambda trainer: {}

    def _topk(self, trainer, candidates):
        self.topk_calls += 1

    def _last(self, trainer, candidates):
        self.last_calls += 1

    def save_checkpoint(self, trainer):
        self.calls += 1


class TestLightningCallback(unittest.TestCase):
    def test_the_savers_own_bookkeeping_is_preferred(self):
        from ckpt_policy.lightning import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            trainer = _FakeLightningTrainer(root)
            saver = _FakeSaver()
            cb = GovernorCallback(g, saver=saver)
            trainer.global_step = 1
            cb.on_train_batch_end(trainer, None)
            self.assertEqual(saver.topk_calls, 1,
                             "top-k retention and the best pointer are updated only by the saver")
            self.assertEqual(saver.last_calls, 1, "last_model_path is updated by the saver")
            self.assertEqual(saver.calls, 0, "the public save was not used when the routines exist")
            self.assertEqual(trainer.saved, [], "the bare save path was not used")
            self.assertFalse(g.should_save())

    def test_a_version_with_only_the_public_save_is_told_what_it_lost(self):
        from ckpt_policy.lightning import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            trainer = _FakeLightningTrainer(root)
            saver = _FakeSaver(private=False)
            cb = GovernorCallback(g, saver=saver)
            trainer.global_step = 1
            cb.on_train_batch_end(trainer, None)
            self.assertEqual(saver.calls, 1, "the write still happens")
            self.assertTrue(any("top-k retention" in note for note in cb.notes),
                            f"the lost guarantees are not recorded: {cb.notes}")

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

    def test_a_later_window_request_is_not_silenced_by_the_generation(self):
        """A framework write leaves no `gen-*`, so every request would carry the same generation.

        Dedupe has to be by `requestId`; otherwise one write in a four-hour job silences the window
        for the rest of it.
        """
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=0, requestId="r-1")
            first = g.requested()
            self.assertTrue(first, "the first window request is served")
            g.served(first)
            self.assertFalse(g.should_save(), "the same plan is not served twice")
            plan(root, generation=0, requestId="r-2")
            self.assertTrue(g.should_save(),
                            "a new request with the same synthetic generation is still served")


class _ComposerState:
    def __init__(self, root, callbacks):
        import types

        self.timestamp = types.SimpleNamespace(batch=1, epoch=0)
        self.rank = 0
        self.callbacks = callbacks
        self.root = root
        self.fsdp_sharded_state_dict_enabled = False


class _FakeCheckpointSaver:
    """Composer's own saver: folder, rotation and the latest symlink are its business."""

    def __init__(self, root, keep=2):
        self.root = root
        self.keep = keep
        self.all_saved_checkpoints_to_timestamp = {}
        self.rotations = 0

    def _save_checkpoint(self, state, logger):
        path = os.path.join(self.root, f"ep0-ba{state.timestamp.batch}-rank{state.rank}")
        with open(path, "w", encoding="utf-8") as f:
            f.write("ckpt")
        self.all_saved_checkpoints_to_timestamp[path] = state.timestamp.batch

    def _rotate_checkpoints(self, sharding_enabled=False):
        self.rotations += 1
        saved = sorted(self.all_saved_checkpoints_to_timestamp)
        while len(saved) > self.keep:
            os.remove(saved.pop(0))
        self.all_saved_checkpoints_to_timestamp = {
            p: t for p, t in self.all_saved_checkpoints_to_timestamp.items() if os.path.exists(p)}


class TestComposerCallback(unittest.TestCase):
    """Composer is optional: the callback imports it lazily, and the test installs a fake module."""

    def setUp(self):
        import types

        self.calls = []
        composer = types.ModuleType("composer")
        utils = types.ModuleType("composer.utils")
        checkpoint = types.ModuleType("composer.utils.checkpoint")
        callbacks = types.ModuleType("composer.callbacks")
        saver = types.ModuleType("composer.callbacks.checkpoint_saver")

        def save_checkpoint(state, filename="ep{epoch}-ba{batch}-rank{rank}", **kwargs):
            self.calls.append((state, filename, kwargs))

        checkpoint.save_checkpoint = save_checkpoint
        saver.CheckpointSaver = _FakeCheckpointSaver
        utils.checkpoint = checkpoint
        callbacks.checkpoint_saver = saver
        composer.utils = utils
        composer.callbacks = callbacks
        self._saved = {name: sys.modules.get(name) for name in
                       ("composer", "composer.utils", "composer.utils.checkpoint",
                        "composer.callbacks", "composer.callbacks.checkpoint_saver")}
        sys.modules["composer"] = composer
        sys.modules["composer.utils"] = utils
        sys.modules["composer.utils.checkpoint"] = checkpoint
        sys.modules["composer.callbacks"] = callbacks
        sys.modules["composer.callbacks.checkpoint_saver"] = saver

    def tearDown(self):
        for name, module in self._saved.items():
            if module is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = module

    def test_nothing_is_written_while_the_plan_is_silent(self):
        from ckpt_policy.composer import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            saver = _FakeCheckpointSaver(root)
            state = _ComposerState(root, [saver])
            cb = GovernorCallback(g)
            cb.init(state, None)
            for _ in range(100):
                cb.batch_end(state, None)
            self.assertEqual(saver.all_saved_checkpoints_to_timestamp, {})

    def test_the_savers_own_routine_is_used_and_the_declaration_sits_beside_it(self):
        from ckpt_policy.composer import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            saver = _FakeCheckpointSaver(root)
            state = _ComposerState(root, [saver])
            cb = GovernorCallback(g)
            cb.init(state, None)
            cb.batch_end(state, None)
            self.assertEqual(len(saver.all_saved_checkpoints_to_timestamp), 1,
                             "the saver's own routine writes the copy")
            self.assertEqual(saver.rotations, 1, "the saver's own rotation is asked to run")
            [path] = saver.all_saved_checkpoints_to_timestamp
            self.assertTrue(os.path.exists(path + ".resume.json"),
                            "the declaration sits beside the artifact the saver wrote")
            self.assertFalse(g.should_save())
            self.assertEqual(cb.saves, 1)

    def test_the_end_of_training_is_recorded(self):
        from ckpt_policy.composer import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            cb = GovernorCallback(g)
            cb.fit_end(object(), None)
            self.assertEqual(glue(root, "ending"), "1")

    def test_without_a_saver_the_write_happens_and_the_loss_is_named(self):
        from ckpt_policy.composer import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            plan(root, generation=7)
            cb = GovernorCallback(g)
            cb.batch_end(object(), None)
            self.assertEqual(len(self.calls), 1, "the write still happened")
            self.assertEqual(len(cb.notes), 2, "and both lost guarantees were named")
            self.assertIn("retention and the latest pointer are yours", cb.notes[0])
            self.assertIn("no declaration was written", cb.notes[1])


class TestDeclare(unittest.TestCase):
    """`declare` writes the one statement a restore is judged against, in one of two places."""

    def test_a_directory_gets_resume_json_with_a_dot_target(self):
        with tempfile.TemporaryDirectory() as root:
            ckpt = os.path.join(root, "checkpoint-1000")
            os.makedirs(ckpt)
            g = Governor(root)
            g.declare(ckpt, step=1000, format="hf-trainer",
                      state={"model": True, "optimizer": True, "scheduler": True,
                             "rng": True, "dataloader": False},
                      requires=["model", "optimizer", "scheduler", "rng"],
                      caveats=["data-position-not-saved"])
            with open(os.path.join(ckpt, "resume.json"), encoding="utf-8") as f:
                doc = json.load(f)
            self.assertEqual(doc["target"], ".")
            self.assertEqual(doc["requires"], ["model", "optimizer", "scheduler", "rng"])
            self.assertEqual(doc["caveats"], ["data-position-not-saved"])
            self.assertFalse(doc["state"]["dataloader"])

    def test_an_artifact_gets_a_declaration_beside_it(self):
        with tempfile.TemporaryDirectory() as root:
            ckpt = os.path.join(root, "last.ckpt")
            with open(ckpt, "w", encoding="utf-8") as f:
                f.write("weights")
            g = Governor(root)
            g.declare(ckpt, step=7)
            path = ckpt + ".resume.json"
            self.assertTrue(os.path.exists(path), path)
            with open(path, encoding="utf-8") as f:
                doc = json.load(f)
            self.assertEqual(doc["target"], "last.ckpt")
            self.assertEqual(doc["requires"], ["model", "optimizer", "scheduler", "rng"])

    def test_an_older_reader_sees_the_same_schema(self):
        """The file stays schema 1: a new field is optional, never a new version."""
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root)
            g.declare(root, step=3)
            with open(os.path.join(root, "resume.json"), encoding="utf-8") as f:
                doc = json.load(f)
            self.assertEqual(doc["schema"], 1)


class _Args:
    def __init__(self, output_dir, save_only_model=False):
        self.output_dir = output_dir
        self.save_only_model = save_only_model


class _State:
    global_step = 1000


class _Control:
    should_save = False


class TestHuggingFaceDeclares(unittest.TestCase):
    def test_on_save_says_four_states_and_no_data_position(self):
        from ckpt_policy.hf import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            ckpt = os.path.join(root, "checkpoint-1000")
            os.makedirs(ckpt)
            cb = GovernorCallback(Governor(root))
            cb.on_save(_Args(root), _State(), _Control())
            with open(os.path.join(ckpt, "resume.json"), encoding="utf-8") as f:
                doc = json.load(f)
            self.assertEqual(doc["requires"], ["model", "optimizer", "scheduler", "rng"])
            self.assertFalse(doc["state"]["dataloader"])
            self.assertIn("data-position-not-saved", doc["caveats"])

    def test_save_only_model_turns_optimizer_into_a_fact(self):
        from ckpt_policy.hf import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            ckpt = os.path.join(root, "checkpoint-1000")
            os.makedirs(ckpt)
            cb = GovernorCallback(Governor(root))
            cb.on_save(_Args(root, save_only_model=True), _State(), _Control())
            with open(os.path.join(ckpt, "resume.json"), encoding="utf-8") as f:
                doc = json.load(f)
            for name in ("optimizer", "scheduler", "rng"):
                self.assertFalse(doc["state"][name], name)


class TestLightningDeclares(unittest.TestCase):
    def test_the_saver_path_is_declared(self):
        from ckpt_policy.lightning import GovernorCallback

        class Saver:
            dirpath = ""

            def save_checkpoint(self, trainer):
                with open(os.path.join(self.dirpath, "ckpt-step-5.ckpt"), "w", encoding="utf-8") as f:
                    f.write("weights")

        class Trainer:
            global_step = 5
            default_root_dir = ""

        with tempfile.TemporaryDirectory() as root:
            plan(root, generation=5)
            g = Governor(root)
            saver = Saver()
            saver.dirpath = root
            cb = GovernorCallback(g, saver=saver)
            trainer = Trainer()
            trainer.default_root_dir = root
            cb.on_train_batch_end(trainer, None)
            path = os.path.join(root, "ckpt-step-5.ckpt.resume.json")
            self.assertTrue(os.path.exists(path), path)
            with open(path, encoding="utf-8") as f:
                doc = json.load(f)
            self.assertEqual(doc["target"], "ckpt-step-5.ckpt")
            self.assertEqual(doc["step"], 5)
            self.assertEqual(doc["requires"], ["model", "optimizer", "scheduler", "rng"])


class TestComposerDeclares(unittest.TestCase):
    def _install_fake_composer(self, run_dir):
        import types

        mod = types.ModuleType("composer.utils.checkpoint")

        def save_checkpoint(state, **kwargs):
            name = kwargs["filename"].format(epoch=state.epoch, batch=state.batch, rank=state.rank)
            path = os.path.join(run_dir, name)
            with open(path, "w", encoding="utf-8") as f:
                f.write("shard")
            return path

        mod.save_checkpoint = save_checkpoint
        sys.modules["composer"] = types.ModuleType("composer")
        sys.modules["composer.utils"] = types.ModuleType("composer.utils")
        sys.modules["composer.utils.checkpoint"] = mod
        return ["composer", "composer.utils", "composer.utils.checkpoint"]

    def _state(self, run_dir, rank):
        import types

        state = types.SimpleNamespace(epoch=1, batch=7, rank=rank, run_dir=run_dir,
                                      timestamp=types.SimpleNamespace(epoch=1, batch=7))
        return state

    def test_rank_zero_writes_one_declaration_beside_the_episode(self):
        from ckpt_policy.composer import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            keys = self._install_fake_composer(root)
            try:
                plan(root, generation=9)
                g = Governor(root)
                cb = GovernorCallback(g)
                cb.batch_end(self._state(root, 0), None)
                path = os.path.join(root, "ep1-ba7-rank0.resume.json")
                self.assertTrue(os.path.exists(path), path)
                with open(path, encoding="utf-8") as f:
                    doc = json.load(f)
                self.assertEqual(doc["target"], "ep1-ba7-rank0")
                self.assertEqual(doc["step"], 7)
                self.assertEqual(doc["requires"], ["model", "optimizer", "scheduler", "rng"])
            finally:
                for key in keys:
                    sys.modules.pop(key, None)

    def test_a_non_leader_does_not_speak_for_the_episode(self):
        from ckpt_policy.composer import GovernorCallback

        with tempfile.TemporaryDirectory() as root:
            keys = self._install_fake_composer(root)
            try:
                plan(root, generation=9)
                g = Governor(root)
                cb = GovernorCallback(g)
                cb.batch_end(self._state(root, 1), None)
                self.assertFalse(os.path.exists(os.path.join(root, "ep1-ba7-rank1.resume.json")))
            finally:
                for key in keys:
                    sys.modules.pop(key, None)


class TestRankOwnership(unittest.TestCase):
    """The leader owns every team-level file; a rank owns only its own `ranks/<rank>/step`."""

    def test_a_non_leader_writes_only_its_own_step(self):
        with tempfile.TemporaryDirectory() as root:
            lead = Governor(root, rank=0)
            follower = Governor(root, rank=1)
            lead.step(100)
            follower.step(96)
            self.assertEqual(glue(root, "step"), "100")
            with open(os.path.join(root, BASE_DIR, "ranks", "1", "step"), encoding="utf-8") as f:
                self.assertEqual(f.read().strip(), "96")
            with open(os.path.join(root, BASE_DIR, "ranks", "0", "step"), encoding="utf-8") as f:
                self.assertEqual(f.read().strip(), "100", "the leader reports its own step too")

    def test_a_non_leader_is_refused_a_team_level_file(self):
        with tempfile.TemporaryDirectory() as root:
            g = Governor(root, rank=1)
            for call in (g.ending, g.on_sigterm, lambda: g.declare(root, step=1)):
                with self.assertRaises(RuntimeError) as ctx:
                    call()
                self.assertIn("lead-only file", str(ctx.exception))

    def test_rank0_is_the_lead_whatever_the_number(self):
        with tempfile.TemporaryDirectory() as root:
            self.assertTrue(Governor(root, rank=0).rank0)
            self.assertFalse(Governor(root, rank0=False).rank0)
            self.assertEqual(Governor(root, rank0=False).rank, 1)


if __name__ == "__main__":
    unittest.main()

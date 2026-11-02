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

from ckptctl_pytorch import Governor, Saves, World  # noqa: E402
from ckptctl_pytorch.glue import BASE_DIR  # noqa: E402


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
            self.assertEqual(m["writer"], "ckptctl-pytorch")
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


if __name__ == "__main__":
    unittest.main()

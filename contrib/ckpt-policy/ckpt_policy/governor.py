"""The training loop's half of the contract, in the smallest API that can carry it.

    g = Governor("/ckpt/job-1", world=World(ranks=8), saves=Saves())
    for step, batch in loader:
        g.step(step)
        if g.requested():
            save(g.generation())          # your library writes the shards
            g.sealed(...)                 # we hash them and write the metadata
"""

import json
import os
import shutil
import signal
import subprocess
import time

from .glue import BASE_DIR, base36, write_text, write_atomic, read_text
from .manifest import build_manifest, write_manifest, write_resume

PLAN_SCHEMA = 2
ENGINE_ENV = "CKPTCTL_ENGINE"


def engine_path() -> str | None:
    """The engine this client should use: the bundled one first, then `CKPTCTL_ENGINE`, then PATH.

    The wheel carries the engine (decision 0028, P1.1), so a `pip install` is the whole install; the
    other two sources exist for development and for deployments that already have `ckptctl` on PATH.
    """
    bundled = os.path.dirname(os.path.abspath(__file__))
    for name in ("ckptctl", "ckptctl.exe"):
        candidate = os.path.join(bundled, "_bin", name)
        if os.path.exists(candidate):
            return candidate
    override = os.environ.get(ENGINE_ENV)
    if override:
        return override
    return shutil.which("ckptctl")


class World:
    """The topology the run is in, and whether the checkpoint can be read under another."""

    def __init__(self, ranks: int = 1, tp: int = 1, pp: int = 1, dp: int = 1, reshardable: bool = False):
        self.ranks, self.tp, self.pp, self.dp = ranks, tp, pp, dp
        self.reshardable = reshardable

    def as_dict(self) -> dict:
        return {"ranks": self.ranks, "tp": self.tp, "pp": self.pp, "dp": self.dp,
                "reshardable": self.reshardable}


class Saves:
    """What the library saves. `False` is a fact a restore is judged against, not a defect."""

    def __init__(self, model=True, optimizer=True, scheduler=True, rng=True, dataloader=True,
                 scaler=False, ema=False):
        self.model, self.optimizer, self.scheduler = model, optimizer, scheduler
        self.rng, self.dataloader, self.scaler, self.ema = rng, dataloader, scaler, ema

    def as_dict(self) -> dict:
        return {"model": self.model, "optimizer": self.optimizer, "scheduler": self.scheduler,
                "rng": self.rng, "dataloader": self.dataloader, "scaler": self.scaler, "ema": self.ema}


class Governor:
    def __init__(self, root: str, *, format: str = "dcp", world: World | None = None,
                 saves: Saves | None = None, rank0: bool = True, members: int | None = None,
                 expected_bytes: int | None = None, writer: str = "ckpt-policy",
                 step_baseline_ms: int = 0, start_engine: bool = False,
                 engine: str | None = None, engine_every: str = "1s",
                 engine_window: str | None = None, engine_args: list[str] | None = None):
        self.root = root
        self.format = format
        self.world = world or World()
        self.saves = saves or Saves()
        self.writer = writer
        self.rank0 = rank0
        self._served: set[int] = set()
        self._stopping_at: float | None = None
        self._grace_seconds: int = 0
        self._last_write = time.monotonic()
        self._last_step = time.monotonic()
        self._engine: subprocess.Popen | None = None
        self._engine_log = None
        self._engine_bin = engine
        self._engine_every = engine_every
        self._engine_window = engine_window
        self._engine_args = list(engine_args or [])

        os.makedirs(os.path.join(root, BASE_DIR), exist_ok=True)
        if not rank0:
            return
        # What the controller can only learn from the job. Absent is fine; unreadable is impossible.
        write_text(root, "boot", base36(time.time_ns()))
        write_text(root, "members", "".join(f"{i}\n" for i in range(members or self.world.ranks)))
        if expected_bytes:
            write_text(root, "expected-bytes", str(int(expected_bytes)))
        if step_baseline_ms:
            write_text(root, "step-baseline", str(int(step_baseline_ms)))
        if start_engine:
            self.start_engine()

    # -- the engine -------------------------------------------------------------------------------

    def start_engine(self) -> subprocess.Popen:
        """Start the Go engine for this directory, once. The policy stays in Go (decision 0028).

        The engine writes `.ckpt-governor/state.json`; this client reads it. If the engine dies the
        plan stops being refreshed and `should_save()` becomes false, which is the degradation the
        contract asks for: the job still writes at the end and on an announced stop, and the caller
        never has to know why.
        """
        if self._engine is not None:
            return self._engine
        path = self._engine_bin or engine_path()
        if not path or not os.path.exists(path):
            raise RuntimeError(
                "no checkpoint-governor engine: the wheel should carry one; "
                "set CKPTCTL_ENGINE to a ckptctl binary, or `pip install` the wheel built by hack/build-wheel.sh"
            )
        if not os.access(path, os.X_OK):
            try:
                os.chmod(path, 0o755)
            except OSError:
                pass
        os.makedirs(os.path.join(self.root, BASE_DIR), exist_ok=True)
        log = open(os.path.join(self.root, BASE_DIR, "engine.log"), "ab")
        args = [path, "run", "--dir", self.root, "--every", self._engine_every]
        if self._engine_window:
            args += ["--window", self._engine_window]
        args += self._engine_args
        self._engine_log = log
        self._engine = subprocess.Popen(args, stdout=log, stderr=log, close_fds=True)
        return self._engine

    def engine_running(self) -> bool:
        """True while the engine is alive. A dead engine is not an error here; the plan simply ages."""
        return self._engine is not None and self._engine.poll() is None

    def should_save(self) -> bool:
        """Whether the plan asks for a write now. The whole of the write side, one line."""
        return self.requested() != 0

    def close(self, timeout: float = 5.0) -> None:
        """Stop the engine this client started. Safe to call twice; a client that never started one
        does nothing."""
        if self._engine is not None:
            if self._engine.poll() is None:
                self._engine.terminate()
                try:
                    self._engine.wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    self._engine.kill()
                    self._engine.wait()
            self._engine = None
        if self._engine_log is not None:
            self._engine_log.close()
            self._engine_log = None

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        self.close()
        return False

    # -- the loop -----------------------------------------------------------------------------

    def step(self, n: int) -> None:
        """Say where the run is. One call per step; it is the whole of the write side."""
        if not self.rank0:
            return
        now = time.monotonic()
        write_text(self.root, "step", str(int(n)))
        if self._stopping_at is not None:
            left = max(0, int(self._grace_seconds - (now - self._stopping_at)))
            write_text(self.root, "seconds-left", str(left))
        self._last_step = now

    def requested(self) -> int:
        """The generation the governor asked for, or 0. A request is acted on at most once."""
        plan = self._plan()
        if not plan:
            return 0
        if plan.get("schema") != PLAN_SCHEMA:
            return 0
        if plan.get("action") != "save":
            return 0
        if int(plan.get("expiresAt") or 0) <= int(time.time()):
            return 0  # a plan that says nothing about liveness is not a request
        gen = int(plan.get("generation") or 0)
        if gen <= 0 or gen in self._served:
            return 0
        return gen

    def generation(self) -> int:
        """The generation to write into: what the plan asked for, else one past the newest seal."""
        return self.requested() or (newest_generation(self.root) + 1)

    def sealed(self, generation: int, step: int, shards: list[str], *, base: str | None = None,
               loader: str | None = None, partial: list[str] | None = None) -> dict:
        """Seal a generation: hash the shards the library wrote, then write manifest, then declaration."""
        if not self.rank0:
            return {}
        gen_dir = os.path.join(self.root, f"gen-{generation:04d}")
        base = base or gen_dir
        manifest = build_manifest(generation, step,
                                  time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                                  self.writer, shards, base)
        write_manifest(gen_dir, manifest)
        write_resume(gen_dir, generation=generation, step=step, format=self.format,
                     world=self.world.as_dict(), state=self.saves.as_dict(),
                     partial=partial, loader=loader)
        self._served.add(generation)
        self._last_write = time.monotonic()
        return manifest

    def resumed(self, generation: int, step: int, seconds: float | None = None) -> None:
        """Say what the loader adopted, right after it returns. This is what makes a restart
        distinguishable from a resume from outside (decision 0026)."""
        if not self.rank0:
            return
        write_text(self.root, "resumed-generation", str(int(generation)))
        write_text(self.root, "resumed-step", str(int(step)))
        if seconds is not None:
            write_text(self.root, "load-seconds", f"{seconds:.3f}")

    def ending(self) -> None:
        """Training is over: the work that exists now is the work that matters."""
        if self.rank0:
            write_text(self.root, "ending", "1")

    def may_exit(self) -> bool:
        """The controller's answer to the final write. A job that releases itself has no protocol."""
        return os.path.exists(os.path.join(self.root, BASE_DIR, "may-exit"))

    def on_sigterm(self, grace_seconds: int = 30) -> None:
        """Record the deadline the platform gave us. The job owns it; we never invent a second one."""
        if not self.rank0:
            return
        self._grace_seconds = int(grace_seconds)
        self._stopping_at = time.monotonic()
        write_text(self.root, "stopping", "1")
        write_text(self.root, "seconds-left", str(self._grace_seconds))

    # -- internals ----------------------------------------------------------------------------

    def _plan(self) -> dict:
        raw = read_text(self.root, "state.json")
        if raw is None:
            return {}
        try:
            return json.loads(raw)
        except ValueError:
            return {}


def newest_generation(root: str) -> int:
    """The highest sealed generation, or 0. A directory without a manifest is a write in flight."""
    best = 0
    try:
        entries = os.listdir(root)
    except OSError:
        return 0
    for name in entries:
        if not name.startswith("gen-"):
            continue
        try:
            n = int(name[4:])
        except ValueError:
            continue
        if os.path.exists(os.path.join(root, name, "manifest.json")) and n > best:
            best = n
    return best

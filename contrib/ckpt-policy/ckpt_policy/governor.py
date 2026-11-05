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

# What a framework checkpoint can carry. Frameworks do not save a data-loader position — they re-iterate
# the dataset and skip — so a declaration that demanded one would refuse every copy they ever write.
FRAMEWORK_REQUIRES = ("model", "optimizer", "scheduler", "rng")

# Each rank's own progress lives here. It is not `members/`: that name is already the launcher's
# roster file, and a file and a directory cannot share it. Single-writer jobs never create this.
RANKS_DIR = "ranks"


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
                 saves: Saves | None = None, rank0: bool = True, rank: int = 0,
                 members: int | None = None,
                 expected_bytes: int | None = None, writer: str = "ckpt-policy",
                 step_baseline_ms: int = 0, start_engine: bool = False,
                 engine: str | None = None, engine_every: str = "1s",
                 engine_window: str | None = None, engine_args: list[str] | None = None):
        self.root = root
        self.format = format
        self.world = world or World()
        self.saves = saves or Saves()
        self.writer = writer
        # `rank` is this process's own number; `rank0=False` with no rank means "not the leader".
        # The leader owns every team-level file; a non-leader owns only `members/<rank>/`.
        self.rank = int(rank) if rank0 else int(rank or 1)
        self.rank0 = self.rank == 0
        self._served: set[int] = set()
        # A request is identified by its `requestId` (decision 0017), because a plan for a first
        # checkpoint carries generation 0: there is no copy yet to number.
        self._served_requests: set[str] = set()
        self._pending_request: str = ""
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

    def served(self, generation: int) -> None:
        """Record that the plan's request was written by the framework's own path.

        `sealed()` does this as part of writing our metadata, but a framework that writes its own
        checkpoint format (Lightning `.ckpt`, Composer, a custom saver) never calls `sealed()`. Dedupe
        is by `requestId`: a framework write leaves no `gen-*` on disk, so the generation number alone
        would silence every later window request. It records, it does not write.
        """
        if self._pending_request:
            self._served_requests.add(self._pending_request)
            self._pending_request = ""
            return
        # A plan without a requestId cannot be told apart from its own refresh; the generation is the
        # only key left.
        self._served.add(int(generation))

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

    def _lead_only(self, name: str) -> None:
        """Refuse a team-level write from a rank that does not own it.

        Silently ignoring it would let eight ranks overwrite one `step` and the controller would see
        whichever wrote last — possibly the one that is behind. An error names the file and the rank.
        """
        if not self.rank0:
            raise RuntimeError(
                f"lead-only file: {name} belongs to rank 0; this process is rank {self.rank}")

    def step(self, n: int) -> None:
        """Say where this rank is. One call per step; it is the whole of the write side.

        Every rank owns its own cell, including the leader: the controller compares members, and a
        leader that only wrote the team-level file would look like the only member. The team-level
        `step` stays for the single-writer path and every existing reader.
        """
        write_text(self.root, os.path.join(RANKS_DIR, str(self.rank), "step"), str(int(n)))
        if self.rank0:
            write_text(self.root, "step", str(int(n)))
        now = time.monotonic()
        if self._stopping_at is not None:
            left = max(0, int(self._grace_seconds - (now - self._stopping_at)))
            if self.rank0:
                write_text(self.root, "seconds-left", str(left))
        self._last_step = now

    def requested(self) -> int:
        """The generation the governor asked for, or 0. A request is acted on at most once.

        Dedupe is by `requestId` (decision 0017), not by the generation. A plan for a first checkpoint
        carries `generation: 0` — there is no copy yet to number — and a client that read `0` as
        "nothing was asked" would ignore the first write of every job.
        """
        plan = self._plan()
        if not plan:
            return 0
        if plan.get("schema") != PLAN_SCHEMA:
            return 0
        if plan.get("action") != "save":
            return 0
        if int(plan.get("expiresAt") or 0) <= int(time.time()):
            return 0  # a plan that says nothing about liveness is not a request
        key = str(plan.get("requestId") or "")
        gen = int(plan.get("generation") or 0)
        if gen <= 0:
            gen = newest_generation(self.root) + 1
        if key and key in self._served_requests:
            return 0
        if not key and gen in self._served:
            return 0
        self._pending_request = key
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
        if self._pending_request:
            self._served_requests.add(self._pending_request)
            self._pending_request = ""
        self._last_write = time.monotonic()
        return manifest

    def declare(self, path: str, *, step: int, state: dict | None = None,
                format: str = "other", requires: list[str] | None = None,
                target: str | None = None, caveats: list[str] | None = None) -> None:
        """Say what a framework-written checkpoint holds, beside that checkpoint.

        The framework owns the layout and the bytes; we own the statement a restore is judged
        against, because nobody else writes one. `state` defaults to this client's configured
        `Saves`, which is what the framework was asked to write.

        `path` is either a directory (Hugging Face's `checkpoint-<step>/`) or the artifact itself
        (Lightning's `*.ckpt`). A directory gets `<dir>/resume.json` with `target: "."`; an artifact
        gets `<artifact>.resume.json` with `target: <file name>`. Those are the only two places a
        declaration may live.

        `requires` defaults to the four states a framework checkpoint can actually carry: model,
        optimizer, scheduler and rng. Frameworks re-iterate the loader instead of saving a data
        position, so judging them against a dataloader they never had would refuse every copy.
        """
        self._lead_only("resume.json")
        is_dir = os.path.isdir(path)
        target = "." if target is None and is_dir else target
        if target is None:
            target = os.path.basename(path)
        if target == ".":
            out_dir, file_name = path, "resume.json"
        else:
            out_dir, file_name = os.path.dirname(path) or ".", target + ".resume.json"
            if is_dir:
                out_dir = path
        os.makedirs(out_dir, exist_ok=True)
        write_resume(out_dir, generation=int(step), step=int(step), format=format,
                     world=self.world.as_dict(),
                     state=state or self.saves.as_dict(),
                     requires=requires if requires is not None else list(FRAMEWORK_REQUIRES),
                     target=target, caveats=caveats, loader=None, file_name=file_name)

    def resumed(self, generation: int, step: int, seconds: float | None = None) -> None:
        """Say what the loader adopted, right after it returns. This is what makes a restart
        distinguishable from a resume from outside (decision 0026)."""
        self._lead_only("resumed-generation")
        write_text(self.root, "resumed-generation", str(int(generation)))
        write_text(self.root, "resumed-step", str(int(step)))
        if seconds is not None:
            write_text(self.root, "load-seconds", f"{seconds:.3f}")

    def ending(self) -> None:
        """Training is over: the work that exists now is the work that matters."""
        self._lead_only("ending")
        write_text(self.root, "ending", "1")

    def may_exit(self) -> bool:
        """The controller's answer to the final write. A job that releases itself has no protocol."""
        return os.path.exists(os.path.join(self.root, BASE_DIR, "may-exit"))

    def on_sigterm(self, grace_seconds: int = 30) -> None:
        """Record the deadline the platform gave us. The job owns it; we never invent a second one."""
        self._lead_only("stopping")
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

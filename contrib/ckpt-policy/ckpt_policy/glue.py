"""Small, boring file operations shared by the governor and the manifest writer.

Every write is a temporary name followed by a rename, for the same reason the Go side does it: a reader
must never see half a step number or half a manifest, and a half-written declaration is a declaration
that was never made (decision 0026).
"""

import os
import tempfile

BASE_DIR = ".ckpt-governor"


def base36(n: int) -> str:
    """The encoding the Go trainer uses for the boot stamp, so both writers leave the same shape."""
    if n < 0:
        raise ValueError("base36 needs a non-negative number")
    digits = "0123456789abcdefghijklmnopqrstuvwxyz"
    if n == 0:
        return "0"
    out = []
    while n:
        n, r = divmod(n, 36)
        out.append(digits[r])
    return "".join(reversed(out))


def write_atomic(path: str, data: bytes) -> None:
    """Write through a temporary name in the same directory, then rename."""
    directory = os.path.dirname(path)
    os.makedirs(directory, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=directory, prefix=".write-")
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def write_text(root: str, name: str, text: str) -> None:
    write_atomic(os.path.join(root, BASE_DIR, name), text.encode("utf-8"))


def read_text(root: str, name: str) -> str | None:
    try:
        with open(os.path.join(root, BASE_DIR, name), "r", encoding="utf-8") as f:
            return f.read().strip()
    except OSError:
        return None

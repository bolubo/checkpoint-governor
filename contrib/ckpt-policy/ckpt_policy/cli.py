"""The `ckptctl` command a `pip install` provides.

The wheel already carries the engine at `ckpt_policy/_bin/ckptctl`; without this entry point the command
exists only if the user also runs `install.sh`, which the install page says is unnecessary. This module
execs the bundled binary, so the command and the client always move together.
"""

import os
import sys

from .governor import engine_path


def main() -> int:
    path = engine_path()
    if not path:
        print("ckptctl: the wheel carries no engine; set CKPTCTL_ENGINE to a ckptctl binary",
              file=sys.stderr)
        return 2
    try:
        os.execv(path, [path, *sys.argv[1:]])
    except OSError as exc:
        print(f"ckptctl: cannot run {path}: {exc}", file=sys.stderr)
        return 2
    return 0  # unreachable: execv replaces the process

"""The one-line integration: a training loop tells the governor where it is, and the governor tells it
when a write is worth doing.

The package has no runtime dependencies. `torch` appears only in `examples/`, because the library that
writes the shards is the user's, not ours (decision 0025).
"""

from .governor import Governor, Saves, World
from .manifest import write_manifest, write_resume

# Each framework's callback lives in its own module (`ckpt_policy.hf`, `ckpt_policy.lightning`), so a
# name collision cannot hide which one a program imported.
__all__ = ["Governor", "Saves", "World", "write_manifest", "write_resume"]
__version__ = "0.23.0"

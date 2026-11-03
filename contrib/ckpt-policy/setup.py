"""The wheel carries a platform binary, so its tag has to name the platform.

A `py3-none-any` wheel would install on macOS and fail at the first `start_engine()`, because the
engine inside was built for linux/amd64. `root_is_pure = False` makes the tag platform-specific, while
the ABI tag stays `none`: this is a subprocess, not a CPython extension.

The platform tag is `manylinux_2_17_*` rather than the raw `linux_*` that setuptools derives. PyPI
refuses the raw one — `400 Binary wheel ... has an unsupported platform tag 'linux_x86_64'` — and it
is right to: `linux_x86_64` promises nothing about which Linux. The engine is a static Go binary
(`CGO_ENABLED=0`), so it carries no glibc symbols at all; `manylinux_2_17` is a claim about the host
pip will install it on, not a claim about linkage that does not exist.
"""

from setuptools import setup
from wheel.bdist_wheel import bdist_wheel


def portable(platform: str) -> str:
    if platform.startswith("linux_"):
        return "manylinux_2_17_" + platform[len("linux_") :]
    return platform


class PlatformWheel(bdist_wheel):
    def finalize_options(self):
        super().finalize_options()
        self.root_is_pure = False

    def get_tag(self):
        _, _, platform = super().get_tag()
        return "py3", "none", portable(platform)


setup(cmdclass={"bdist_wheel": PlatformWheel})

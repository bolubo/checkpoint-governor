"""The wheel carries a platform binary, so its tag has to name the platform.

A `py3-none-any` wheel would install on macOS and fail at the first `start_engine()`, because the
engine inside was built for linux/amd64. `root_is_pure = False` makes the tag platform-specific, while
the ABI tag stays `none`: this is a subprocess, not a CPython extension.
"""

from setuptools import setup
from wheel.bdist_wheel import bdist_wheel


class PlatformWheel(bdist_wheel):
    def finalize_options(self):
        super().finalize_options()
        self.root_is_pure = False

    def get_tag(self):
        _, _, platform = super().get_tag()
        return "py3", "none", platform


setup(cmdclass={"bdist_wheel": PlatformWheel})

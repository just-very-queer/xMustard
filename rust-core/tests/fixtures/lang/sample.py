import os
import os.path as osp
from pkg.mod import helper, other as alias_other
from pkg.star import *

# COMMENT_ONLY_WORD must never become a reference
LIMIT = 10


@decorate
def top(a, b=1):
    """Docstring with STRING_ONLY_WORD."""
    return helper(a) + LIMIT


class Outer(Base):
    """Container."""

    def method(self, v):
        self.value = v
        if check(v):
            return osp.join("x", v)

        def local_fn():
            return v

        return local_fn()

    class Inner:
        def deep(self):
            return top(1)


def _private():
    pass

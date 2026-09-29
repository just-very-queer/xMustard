from . import util


class A:
    def who(self):
        return "A"

    def base_only(self):
        return util.twice(1)


class B(A):
    pass


class C(A):
    def who(self):
        return "C"


class D(B, C):
    pass


def probe():
    d = D()
    d.base_only()
    return d.who()

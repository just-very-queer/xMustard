import pkg.util as u
from pkg import Child, twice
from pkg.mro import probe
from ns import thing


def run():
    c = Child()
    c.run()
    u.format_value(2)
    twice(3)
    thing.make()
    probe()
    return u.twice(1)

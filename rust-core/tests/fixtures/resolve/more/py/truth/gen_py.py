"""Ground truth for the Python fixture: the calls between the fixture's own functions
that actually run (sys.setprofile), as (caller, callee) = "path::Qualified.name".

    python3 gen_py.py .. > py_calls.json
"""
import json
import os
import sys

root = os.path.abspath(sys.argv[1])
sys.path.insert(0, root)
pairs = set()


def name_of(frame):
    code = frame.f_code
    path = os.path.abspath(code.co_filename)
    if not path.startswith(root + os.sep) or code.co_name.startswith("<"):
        return None
    owner = frame.f_locals.get("self")
    if owner is not None:
        for cls in type(owner).__mro__:
            fn = cls.__dict__.get(code.co_name)
            if getattr(fn, "__code__", None) is code:
                return f"{os.path.relpath(path, root)}::{cls.__name__}.{code.co_name}"
    return f"{os.path.relpath(path, root)}::{code.co_name}"


def profile(frame, event, _arg):
    if event != "call" or frame.f_back is None:
        return
    callee, caller = name_of(frame), name_of(frame.f_back)
    if callee and caller:
        pairs.add((caller, callee))


import main  # noqa: E402

sys.setprofile(profile)
main.run()
sys.setprofile(None)
print(json.dumps(sorted(pairs), indent=2))

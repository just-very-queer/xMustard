import sys

path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(path).read()
if s.count(old) != 1:
    print("SUBJECT_MISS count=%d" % s.count(old))
    sys.exit(3)
open(path, "w").write(s.replace(old, new, 1))
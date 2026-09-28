#!/bin/sh
# Move the Homebrew formula to a published release: `sh packaging/homebrew/bump.sh v0.2.0`.
#
# Each url line that names the formula's current tag moves to the new tag, and the sha256
# line under it gets that file's checksum. A release archive's checksum comes from the
# release's SHA256SUMS (release.yml wrote it after `shasum -c`); the source tarball's
# comes from downloading it. Which platforms take an archive stays as the formula says;
# the script names the release archives the formula does not use. It refuses unless every
# url resolves, prints the diff, and writes the formula only then. Run it after the draft
# release is published, since the formula's urls must be public. Rerunning it on the
# current tag checks the formula against the release and changes nothing.
set -eu

tag=${1:-}
formula=${2:-$(dirname "$0")/xmustard.rb}
case $tag in *[!0-9A-Za-z.-]*) tag= ;; esac # grep matches per line, so no newline gets past
if ! printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "usage: bump.sh vX.Y.Z[-pre] [formula]" >&2
  exit 2
fi
old=$(sed -n 's/^    version "\(.*\)"$/\1/p' "$formula")
repo=$(sed -n 's/^  homepage "\(.*\)"$/\1/p' "$formula")
if [ -z "$old" ] || [ -z "$repo" ]; then
  echo "refusing: $formula has no stable version or homepage line" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
source_url="$repo/archive/refs/tags/$tag.tar.gz"
curl -fsSL -o "$tmp/SHA256SUMS" "$repo/releases/download/$tag/SHA256SUMS"
curl -fsSL -o "$tmp/source.tar.gz" "$source_url"

# one `sha256  url` line per release file (bump.awk refuses a malformed line)
{
  printf '%s  %s\n' "$(shasum -a 256 "$tmp/source.tar.gz" | cut -d ' ' -f 1)" "$source_url"
  sed "s|  \*\{0,1\}|  $repo/releases/download/$tag/|" "$tmp/SHA256SUMS"
} >"$tmp/table"

cat >"$tmp/bump.awk" <<'AWK'
# every literal `from` in s becomes `to` (versions hold regex metacharacters)
function swap(s, from, to,    out, i) {
  out = ""
  while ((i = index(s, from)) > 0) {
    out = out substr(s, 1, i - 1) to
    s = substr(s, i + length(from))
  }
  return out s
}
function fail(msg) {
  print "refusing: " msg > "/dev/stderr"
  bad = 1
  exit 1
}
NR == FNR {
  if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-f]/) fail("bad checksum line: " $0)
  sha[$2] = $1
  next
}
pending != "" {
  if ($1 != "sha256") fail("url " pending " is not followed by its sha256 line")
  sub(/"[0-9a-f]*"/, "\"" sha[pending] "\"")
  pending = ""
}
$1 == "version" && $2 == "\"" old "\"" { $0 = swap($0, old, new) }
$1 == "url" && index($0, "v" old) {
  $0 = swap($0, "v" old, "v" new)
  u = $2
  gsub(/"/, "", u)
  if (!(u in sha)) fail("the release has no checksum for " u)
  pending = u
  used[u] = 1
  moved++
}
{ print }
END {
  if (bad) exit 1
  if (pending != "") fail("url " pending " is not followed by its sha256 line")
  if (!moved) fail("no url names v" old)
  for (u in sha)
    if (!(u in used)) print "note: the formula does not use " u > "/dev/stderr"
}
AWK
awk -v old="$old" -v new="${tag#v}" -f "$tmp/bump.awk" "$tmp/table" "$formula" >"$tmp/formula.rb"

if cmp -s "$formula" "$tmp/formula.rb"; then
  echo "$formula already matches $tag"
  exit 0
fi
diff -u "$formula" "$tmp/formula.rb" || true
cp "$tmp/formula.rb" "$formula"
echo "updated $formula to $tag; review the diff, then brew install --formula $formula && brew test xmustard"

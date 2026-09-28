#!/bin/sh
# Makes the parity corpus repositories available at their pinned commits under
# research/ (the paths corpus.yaml names). A missing repository is cloned shallowly at
# the pinned commit. An existing clone is never checked out or reset: the commit is only
# fetched into it when it is missing, since xmustard-eval fetches the pinned commit from
# the clone and needs nothing else.
set -eu

root=$(cd "$(dirname "$0")/../../.." && pwd)

fetch() { # name url commit
	dir="$root/research/$1"
	if [ ! -d "$dir/.git" ]; then
		git init -q "$dir"
		git -C "$dir" fetch -q --depth 1 "$2" "$3"
		git -C "$dir" checkout -q --detach "$3"
	elif ! git -C "$dir" cat-file -e "$3^{commit}" 2>/dev/null; then
		git -C "$dir" fetch -q --depth 1 "$2" "$3"
	fi
	git -C "$dir" cat-file -e "$3^{commit}"
	echo "$1: $3 available in $dir"
}

fetch pi-mono https://github.com/badlogic/pi-mono 5fd446ca1843682e8da3fec4ceb71c42f56fbace
fetch cline https://github.com/cline/cline ee59f81706981e0a64c8b32f8f0415c9d39561fa

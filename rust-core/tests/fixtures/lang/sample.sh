#!/bin/bash
source ./lib.sh
. "$HOME/env.sh"

# COMMENT_ONLY_WORD
LIMIT=3

# Doc comment.
build() {
  local out="$1"
  echo "STRING_ONLY_WORD"
  helper "$out" "$LIMIT"
}

function deploy {
  build release
}

deploy

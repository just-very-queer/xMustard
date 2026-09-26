#!/usr/bin/env bash
# D-02 probe driver: each phase runs in its own process under `time` so the peak RSS
# (and, on macOS, the peak physical footprint) belongs to that phase alone.
#   usage: run.sh <probe-binary> <repo-root> <work-dir> [label]
set -euo pipefail
BIN=$1; ROOT=$2; WORK=$3; LABEL=${4:-$(basename "$ROOT")}
mkdir -p "$WORK"
Q="$WORK/$LABEL.known-items.json"
phase() {
  local name=$1; shift
  local tf="$WORK/$LABEL.$name.time"
  if [[ "$(uname)" == "Darwin" ]]; then
    /usr/bin/time -l "$BIN" "$@" >"$WORK/$LABEL.$name.out.json" 2>"$tf"
    local rss fp
    rss=$(awk '/maximum resident set size/ {print $1}' "$tf")
    fp=$(awk '/peak memory footprint/ {print $1}' "$tf")
    printf '{"label":"%s","phase":"%s","time_maxrss_bytes":%s,"time_peak_footprint_bytes":%s,"probe":%s}\n' \
      "$LABEL" "$name" "$rss" "${fp:-null}" "$(cat "$WORK/$LABEL.$name.out.json")"
  else
    /usr/bin/time -v "$BIN" "$@" >"$WORK/$LABEL.$name.out.json" 2>"$tf"
    local kb
    kb=$(awk -F: '/Maximum resident set size/ {gsub(/ /,"",$2); print $2}' "$tf")
    printf '{"label":"%s","phase":"%s","time_maxrss_bytes":%s,"time_peak_footprint_bytes":null,"probe":%s}\n' \
      "$LABEL" "$name" "$((kb * 1024))" "$(cat "$WORK/$LABEL.$name.out.json")"
  fi
}
phase queries queries "$ROOT" "$Q" 400
phase fts5-build-full fts5-build "$ROOT" "$WORK/$LABEL.fts5-full.db" full
phase fts5-query-full-or fts5-query "$WORK/$LABEL.fts5-full.db" "$Q" or
phase fts5-query-full-and fts5-query "$WORK/$LABEL.fts5-full.db" "$Q" and
phase fts5-build-column fts5-build "$ROOT" "$WORK/$LABEL.fts5-column.db" column
phase fts5-query-column-or fts5-query "$WORK/$LABEL.fts5-column.db" "$Q" or
phase tantivy-build tantivy-build "$ROOT" "$WORK/$LABEL.tantivy" 15
phase tantivy-query-or tantivy-query "$WORK/$LABEL.tantivy" "$Q" or
phase tantivy-query-and tantivy-query "$WORK/$LABEL.tantivy" "$Q" and

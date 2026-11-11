#!/usr/bin/env bash
# Fails when the TUN helper's code changed since the previous release tag
# but cmd/lanflow-helper/VERSION did not: an installed helper is replaced
# only when its version differs, so an unbumped version would leave the old
# helper running with the new app.
# Usage: scripts/check-helper-version.sh [ref]   (default: HEAD; needs tags)
set -euo pipefail
cd "$(dirname "$0")/.."
REF="${1:-HEAD}"
PREV="$(git describe --tags --abbrev=0 --match 'v*' "${REF}^" 2>/dev/null || true)"
if [ -z "$PREV" ]; then
  echo "no earlier release tag; nothing to compare"
  exit 0
fi
# The helper's own packages, from the build graph (cmd/lanflow-helper and
# what it imports from this module), plus go.mod/go.sum for its dependencies.
paths=()
while read -r dir; do
  paths+=("${dir#"$PWD"/}")
done < <(go list -deps -f '{{if not .Standard}}{{if eq .Module.Path "lanflow"}}{{.Dir}}{{end}}{{end}}' ./cmd/lanflow-helper | sed '/^$/d')
paths+=(go.mod go.sum)
changed="$(git diff --name-only "$PREV" "$REF" -- "${paths[@]}" | grep -v -e '^cmd/lanflow-helper/VERSION$' -e '_test\.go$' || true)"
if [ -z "$changed" ]; then
  echo "helper unchanged since $PREV"
  exit 0
fi
old="$(git show "$PREV:cmd/lanflow-helper/VERSION" 2>/dev/null | tr -d ' \n' || true)"
new="$(git show "$REF:cmd/lanflow-helper/VERSION" | tr -d ' \n')"
if [ "$old" = "$new" ]; then
  echo "error: the helper changed since $PREV but cmd/lanflow-helper/VERSION is still $new:" >&2
  echo "$changed" >&2
  exit 1
fi
echo "helper $old -> $new since $PREV"

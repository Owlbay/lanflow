#!/usr/bin/env bash
# Builds the app for one platform with mygo, linking in what
# prepare-resources.sh recorded about the bundled helper (its version and,
# on macOS, its SHA-256) through MYGO_LDFLAGS. One platform per call: the
# flags differ per platform, and mygo applies MYGO_LDFLAGS to every target.
# Usage: scripts/build-app.sh <goos> <goarch> [extra mygo build flags]
set -euo pipefail
cd "$(dirname "$0")/.."
GOOS_T="$1"; GOARCH_T="$2"; shift 2
FLAGS_FILE="bin/${GOOS_T}-${GOARCH_T}/app-ldflags"
[ -f "$FLAGS_FILE" ] || { echo "error: $FLAGS_FILE missing; run scripts/prepare-resources.sh $GOOS_T $GOARCH_T first" >&2; exit 1; }
MYGO_LDFLAGS="$(cat "$FLAGS_FILE")" go tool mygo build -platform "${GOOS_T}/${GOARCH_T}" "$@"

#!/usr/bin/env bash
# Puts what the app ships besides itself into resources/ for `mygo build`:
#   resources/geo/                      geo databases (all platforms)
#   resources/<goos>-<goarch>/mihomo     the Mihomo core (verified by fetch-core.sh)
#   resources/<goos>-<goarch>/lanflow-helper   the TUN privileged helper
# and bin/<goos>-<goarch>/app-ldflags: the -X flags that pin the helper's
# version (and on macOS its SHA-256) into the app; scripts/build-app.sh
# passes them to mygo through MYGO_LDFLAGS.
# Usage: scripts/prepare-resources.sh [goos] [goarch]   (default: host)
set -euo pipefail
cd "$(dirname "$0")/.."
GOOS_T="${1:-$(go env GOOS)}"
GOARCH_T="${2:-$(go env GOARCH)}"
EXT=""; [ "$GOOS_T" = windows ] && EXT=".exe"
PLAT="resources/${GOOS_T}-${GOARCH_T}"

# Always run fetch-core.sh for the target: it reuses a cached archive only
# when its SHA-256 still matches, so a stale or tampered bin/ is never packed,
# and it writes the core to bin/<goos>-<goarch>/ for every target.
scripts/fetch-core.sh "$GOOS_T" "$GOARCH_T"
CORE="bin/${GOOS_T}-${GOARCH_T}/mihomo$EXT"
[ -f "$CORE" ] || { echo "error: fetch-core.sh did not produce $CORE" >&2; exit 1; }

mkdir -p "$PLAT" resources/geo
cp "$CORE" "$PLAT/mihomo$EXT"
cp bin/geo/geoip.metadb bin/geo/geosite.dat bin/geo/GeoLite2-ASN.mmdb resources/geo/
# The helper has a version of its own (cmd/lanflow-helper/VERSION), not
# the app's: an installed helper of another version must be reinstalled
# (administrator password) before TUN works, so it changes only when the
# helper does, and app updates leave TUN working.
# scripts/check-helper-version.sh makes a release bump it when its code changed.
VERSION="$(tr -d ' \n' < cmd/lanflow-helper/VERSION)"
GOOS="$GOOS_T" GOARCH="$GOARCH_T" CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=${VERSION}" -o "$PLAT/lanflow-helper$EXT" ./cmd/lanflow-helper
chmod +x "$PLAT/mihomo$EXT" "$PLAT/lanflow-helper$EXT" 2>/dev/null || true

# The app checks the helper it hands to the root installer against
# ExpectedHelperSHA256, so the bytes hashed here must be the bytes shipped.
# mygo build ad-hoc signs Mach-O resources that have no signature yet (the
# Go linker signs only darwin/arm64), which would change the file after it
# was hashed: sign it ad hoc first, so mygo leaves it as it is. A real
# signing identity re-signs every resource; set LANFLOW_NO_HELPER_PIN=1 then,
# and the app hashes the helper right before the administrator prompt.
LDFLAGS="-X lanflow/internal/platform/helper.ExpectedVersion=${VERSION}"
if [ "$GOOS_T" = darwin ] && [ -z "${LANFLOW_NO_HELPER_PIN:-}" ]; then
  if [ "$(uname -s)" = Darwin ]; then
    codesign --force --sign - "$PLAT/lanflow-helper"
  else
    echo "warning: cannot sign on $(uname -s); not pinning the helper's SHA-256" >&2
    PIN_SKIP=1
  fi
  if [ -z "${PIN_SKIP:-}" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      SUM="$(sha256sum "$PLAT/lanflow-helper" | cut -d' ' -f1)"
    else
      SUM="$(shasum -a 256 "$PLAT/lanflow-helper" | cut -d' ' -f1)"
    fi
    LDFLAGS="$LDFLAGS -X lanflow/internal/platform/helper.ExpectedHelperSHA256=${SUM}"
  fi
fi
mkdir -p "bin/${GOOS_T}-${GOARCH_T}"
printf '%s\n' "$LDFLAGS" > "bin/${GOOS_T}-${GOARCH_T}/app-ldflags"
echo "app ldflags: $LDFLAGS"
echo "resources ready for ${GOOS_T}/${GOARCH_T}:"
ls -la "$PLAT" resources/geo

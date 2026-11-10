#!/usr/bin/env bash
# Puts what the app ships besides itself into resources/ for `mygo build`:
#   resources/geo/                      geo databases (all platforms)
#   resources/<goos>-<goarch>/mihomo     the Mihomo core (verified by fetch-core.sh)
#   resources/<goos>-<goarch>/lanflow-helper   the TUN privileged helper
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
VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' mygo.json)"
GOOS="$GOOS_T" GOARCH="$GOARCH_T" CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=${VERSION}" -o "$PLAT/lanflow-helper$EXT" ./cmd/lanflow-helper
chmod +x "$PLAT/mihomo$EXT" "$PLAT/lanflow-helper$EXT" 2>/dev/null || true
echo "resources ready for ${GOOS_T}/${GOARCH_T}:"
ls -la "$PLAT" resources/geo

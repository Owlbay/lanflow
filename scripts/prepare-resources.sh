#!/usr/bin/env bash
# Puts what the app ships besides itself into resources/ for `mygo build`:
#   resources/geo/                      geo databases (all platforms)
#   resources/<goos>-<goarch>/mihomo     the Mihomo core (fetched by fetch-core.sh)
#   resources/<goos>-<goarch>/lanflow-helper   the TUN privileged helper
# Usage: scripts/prepare-resources.sh [goos] [goarch]   (default: host)
set -euo pipefail
cd "$(dirname "$0")/.."
GOOS_T="${1:-$(go env GOOS)}"
GOARCH_T="${2:-$(go env GOARCH)}"
EXT=""; [ "$GOOS_T" = windows ] && EXT=".exe"
PLAT="resources/${GOOS_T}-${GOARCH_T}"

if [ "$GOOS_T-$GOARCH_T" = "$(go env GOOS)-$(go env GOARCH)" ]; then
  [ -x "bin/mihomo$EXT" ] || scripts/fetch-core.sh
  CORE="bin/mihomo$EXT"
else
  scripts/fetch-core.sh "$GOOS_T" "$GOARCH_T"
  CORE="bin/${GOOS_T}-${GOARCH_T}/mihomo$EXT"
  [ -f "$CORE" ] || CORE="bin/mihomo$EXT"
fi

mkdir -p "$PLAT" resources/geo
cp "$CORE" "$PLAT/mihomo$EXT"
cp bin/geo/geoip.metadb bin/geo/geosite.dat bin/geo/GeoLite2-ASN.mmdb resources/geo/
VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' mygo.json)"
GOOS="$GOOS_T" GOARCH="$GOARCH_T" CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=${VERSION}" -o "$PLAT/lanflow-helper$EXT" ./cmd/lanflow-helper
chmod +x "$PLAT/mihomo$EXT" "$PLAT/lanflow-helper$EXT" 2>/dev/null || true
echo "resources ready for ${GOOS_T}/${GOARCH_T}:"
ls -la "$PLAT" resources/geo

#!/usr/bin/env bash
# fetch-core.sh - download the pinned official Mihomo core and geodata.
#
# Usage:
#   scripts/fetch-core.sh [GOOS] [GOARCH]
#
# Defaults to the host platform (via `go env`, falling back to uname). Places
# the core at ./bin/mihomo (./bin/mihomo.exe for windows) and the geodata
# files under ./bin/geo/. Prints the sha256 of every file it downloaded.
#
# Pinned core version: see MIHOMO_VERSION below. Geodata comes from the
# MetaCubeX/meta-rules-dat "latest" release (rolling).
#
# Environment overrides:
#   MIHOMO_VERSION   core tag (default v1.19.33)
#   GITHUB_MIRROR    URL prefix inserted before https://github.com/... (e.g.
#                    https://ghproxy.example.com/) for restricted networks
#   SKIP_GEO=1       do not download geodata
set -euo pipefail

MIHOMO_VERSION="${MIHOMO_VERSION:-v1.19.33}"
GITHUB_MIRROR="${GITHUB_MIRROR:-}"
SKIP_GEO="${SKIP_GEO:-0}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="$REPO_ROOT/bin"
GEO_DIR="$BIN_DIR/geo"

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

host_goos() {
	if command -v go >/dev/null 2>&1; then go env GOOS; return; fi
	case "$(uname -s)" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	MINGW* | MSYS* | CYGWIN*) echo windows ;;
	*) die "cannot detect GOOS; pass it explicitly" ;;
	esac
}

host_goarch() {
	if command -v go >/dev/null 2>&1; then go env GOARCH; return; fi
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) die "cannot detect GOARCH; pass it explicitly" ;;
	esac
}

GOOS="${1:-$(host_goos)}"
GOARCH="${2:-$(host_goarch)}"

case "$GOOS/$GOARCH" in
darwin/arm64 | darwin/amd64) ASSET_EXT="gz"; OUT_NAME="mihomo" ;;
linux/arm64 | linux/amd64) ASSET_EXT="gz"; OUT_NAME="mihomo" ;;
windows/amd64 | windows/arm64) ASSET_EXT="zip"; OUT_NAME="mihomo.exe" ;;
*) die "unsupported platform $GOOS/$GOARCH" ;;
esac

# Official asset naming, e.g. mihomo-darwin-arm64-v1.19.33.gz,
# mihomo-windows-amd64-v1.19.33.zip (the plain amd64 build, not -v1/-v2/-v3).
ASSET="mihomo-${GOOS}-${GOARCH}-${MIHOMO_VERSION}.${ASSET_EXT}"
CORE_URL="${GITHUB_MIRROR}https://github.com/MetaCubeX/mihomo/releases/download/${MIHOMO_VERSION}/${ASSET}"
GEO_BASE="${GITHUB_MIRROR}https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest"
GEO_FILES=(geoip.metadb geosite.dat GeoLite2-ASN.mmdb)

TMP="$(mktemp -d "${TMPDIR:-/tmp}/lanflow-fetch-core.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
	else shasum -a 256 "$1" | awk '{print $1}'; fi
}

download() { # url dest
	log "downloading $1"
	curl -fsSL --retry 3 --retry-delay 2 -o "$2" "$1" || die "download failed: $1"
}

mkdir -p "$BIN_DIR"

# --- core ---------------------------------------------------------------
download "$CORE_URL" "$TMP/$ASSET"
case "$ASSET_EXT" in
gz)
	gunzip -c "$TMP/$ASSET" >"$TMP/$OUT_NAME"
	;;
zip)
	command -v unzip >/dev/null 2>&1 || die "unzip is required to extract $ASSET"
	unzip -q -o "$TMP/$ASSET" -d "$TMP/unzip"
	found="$(find "$TMP/unzip" -type f -name 'mihomo*.exe' | head -n 1)"
	[ -n "$found" ] || die "no mihomo*.exe inside $ASSET"
	mv "$found" "$TMP/$OUT_NAME"
	;;
esac
chmod 0755 "$TMP/$OUT_NAME"

# Verify the binary runs when it targets the host; cross-platform downloads
# are only checked for being non-empty.
if [ "$GOOS" = "$(host_goos)" ] && [ "$GOARCH" = "$(host_goarch)" ]; then
	ver_out="$("$TMP/$OUT_NAME" -v 2>&1 | head -n 1 || true)"
	case "$ver_out" in
	*"Mihomo Meta ${MIHOMO_VERSION}"*) log "verified: $ver_out" ;;
	*) die "downloaded core did not report ${MIHOMO_VERSION}: ${ver_out:-<no output>}" ;;
	esac
else
	[ -s "$TMP/$OUT_NAME" ] || die "downloaded core is empty"
	log "cross-platform download ($GOOS/$GOARCH); skipped -v check"
fi

mv -f "$TMP/$OUT_NAME" "$BIN_DIR/$OUT_NAME"
printf '%s  %s\n' "$(sha256 "$BIN_DIR/$OUT_NAME")" "bin/$OUT_NAME"

# --- geodata ------------------------------------------------------------
if [ "$SKIP_GEO" != "1" ]; then
	mkdir -p "$GEO_DIR"
	for f in "${GEO_FILES[@]}"; do
		download "$GEO_BASE/$f" "$TMP/$f"
		[ -s "$TMP/$f" ] || die "downloaded $f is empty"
		mv -f "$TMP/$f" "$GEO_DIR/$f"
		printf '%s  %s\n' "$(sha256 "$GEO_DIR/$f")" "bin/geo/$f"
	done
fi

log "done: $BIN_DIR/$OUT_NAME (Mihomo $MIHOMO_VERSION, $GOOS/$GOARCH)"

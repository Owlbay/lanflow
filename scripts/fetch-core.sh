#!/usr/bin/env bash
# fetch-core.sh - download the official Mihomo core and geodata, verified.
#
# Usage:
#   scripts/fetch-core.sh [GOOS] [GOARCH]
#
# Defaults to the host platform (via `go env`, falling back to uname).
# Writes:
#   bin/<goos>-<goarch>/mihomo[.exe]   the core for that platform
#   bin/mihomo[.exe]                   a copy, only when the target is the host
#                                      (the end-to-end tests use it)
#   bin/geo/{geoip.metadb,geosite.dat,GeoLite2-ASN.mmdb}
#   bin/cache/                         verified release archives (reused)
#
# Every download is checked against a SHA-256 known before the download
# starts; a mismatch, or no known sum, stops the script and nothing is
# installed:
#   core      the sums of the pinned MIHOMO_VERSION are listed below (taken
#             from the asset digests of the official release,
#             github.com/MetaCubeX/mihomo); any other version uses the
#             `digest` the GitHub API reports for the asset
#   geodata   MetaCubeX/meta-rules-dat publishes only a rolling "latest"
#             release (its release branch is rewritten daily, so there is no
#             tag or commit to pin): the sums come from the GitHub API for
#             GEO_TAG, or from GEOIP_SHA256 / GEOSITE_SHA256 / ASN_SHA256
#             when set (to pin a known set of files)
# The GitHub API is always asked directly (api.github.com), never through
# GITHUB_MIRROR, so a mirror can only serve files whose sums are known.
#
# Environment overrides:
#   MIHOMO_VERSION   core tag (default: the pinned version below)
#   GEO_TAG          meta-rules-dat release tag (default latest)
#   GITHUB_MIRROR    URL prefix inserted before https://github.com/... (e.g.
#                    https://ghproxy.example.com/) for restricted networks
#   GITHUB_TOKEN     sent to api.github.com only (raises the rate limit in CI)
#   SKIP_GEO=1       do not download geodata
set -euo pipefail

PINNED_VERSION="v1.19.33"
MIHOMO_VERSION="${MIHOMO_VERSION:-$PINNED_VERSION}"
GEO_TAG="${GEO_TAG:-latest}"
GITHUB_MIRROR="${GITHUB_MIRROR:-}"
SKIP_GEO="${SKIP_GEO:-0}"

# pinned_sha256 ASSET prints the SHA-256 of an official asset of
# PINNED_VERSION. Keep in sync with internal/app/coresums.go (a test checks).
pinned_sha256() {
	case "$1" in
	mihomo-darwin-amd64-v1.19.33.gz) echo 551ae54ef8d77a5e21f7a8185a59b441fb13a0fb0514fcdef5ca2816f6f50015 ;;
	mihomo-darwin-arm64-v1.19.33.gz) echo 060b64b0f2c9a310831db0fe4adb2878de67bfa606c311813c1f13966aa504b6 ;;
	mihomo-linux-amd64-v1.19.33.gz) echo 132f6f3edf9a65e62f04853aed3358472e0b2b7666daa8419edfa2ddf5bb6323 ;;
	mihomo-linux-arm64-v1.19.33.gz) echo ba408b317080ee265c46cb923a50018c1b82cbd708551cdd46a1e3e307396187 ;;
	mihomo-windows-amd64-v1.19.33.zip) echo 6ab2180875f9f6fff8b19386d760daec257f66b45f72b3afc650634272fa837a ;;
	mihomo-windows-arm64-v1.19.33.zip) echo a2a26869a14634c49f3fbcecc593b498ed8591fbdd7616b24475b3d60b148da3 ;;
	*) return 1 ;;
	esac
}

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="$REPO_ROOT/bin"
GEO_DIR="$BIN_DIR/geo"
CACHE_DIR="$BIN_DIR/cache"

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
PLAT_DIR="$BIN_DIR/$GOOS-$GOARCH"

# Official asset naming, e.g. mihomo-darwin-arm64-v1.19.33.gz,
# mihomo-windows-amd64-v1.19.33.zip (the plain amd64 build, not -v1/-v2/-v3).
ASSET="mihomo-${GOOS}-${GOARCH}-${MIHOMO_VERSION}.${ASSET_EXT}"
CORE_URL="${GITHUB_MIRROR}https://github.com/MetaCubeX/mihomo/releases/download/${MIHOMO_VERSION}/${ASSET}"
GEO_BASE="${GITHUB_MIRROR}https://github.com/MetaCubeX/meta-rules-dat/releases/download/${GEO_TAG}"
GEO_FILES=(geoip.metadb geosite.dat GeoLite2-ASN.mmdb)

TMP="$(mktemp -d "${TMPDIR:-/tmp}/lanflow-fetch-core.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
	else shasum -a 256 "$1" | awk '{print $1}'; fi
}

download() { # url dest
	log "downloading $1"
	curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --retry-delay 2 -o "$2" "$1" || die "download failed: $1"
}

# api_release_json REPO TAG DEST: the release object, straight from
# api.github.com (never mirrored).
api_release_json() {
	local url="https://api.github.com/repos/$1/releases/tags/$2"
	local auth=()
	[ -n "${GITHUB_TOKEN:-}" ] && auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
	log "querying $url"
	curl -fsSL --proto '=https' --tlsv1.2 --retry 3 --retry-delay 2 \
		-H "Accept: application/vnd.github+json" "${auth[@]+"${auth[@]}"}" \
		-o "$3" "$url" || die "GitHub API request failed: $url"
}

# asset_digest JSON NAME prints the hex SHA-256 of asset NAME, or nothing.
asset_digest() {
	local d=""
	if command -v jq >/dev/null 2>&1; then
		d="$(jq -r --arg n "$2" '.assets[] | select(.name == $n) | .digest // empty' "$1")"
	elif command -v python3 >/dev/null 2>&1; then
		d="$(python3 -I -c 'import json,sys
r=json.load(open(sys.argv[1]))
print(next((a.get("digest") or "" for a in r.get("assets",[]) if a.get("name")==sys.argv[2]),""))' "$1" "$2")"
	else
		die "jq or python3 is needed to read the GitHub API answer"
	fi
	case "$d" in
	sha256:*) printf '%s\n' "${d#sha256:}" ;;
	esac
}

is_sha256() { [[ "$1" =~ ^[0-9a-f]{64}$ ]]; }

# fetch_verified URL DEST EXPECTED: download URL to DEST unless DEST already
# has EXPECTED; die when the download does not match.
fetch_verified() {
	if [ -f "$2" ] && [ "$(sha256 "$2")" = "$3" ]; then
		log "cached: $2"
		return
	fi
	download "$1" "$TMP/dl"
	local got
	got="$(sha256 "$TMP/dl")"
	if [ "$got" != "$3" ]; then
		die "SHA-256 mismatch for $1
  expected $3
  got      $got
nothing was installed"
	fi
	mkdir -p "$(dirname "$2")"
	mv -f "$TMP/dl" "$2"
}

# --- core ---------------------------------------------------------------
if [ "$MIHOMO_VERSION" = "$PINNED_VERSION" ] && CORE_SHA="$(pinned_sha256 "$ASSET")"; then
	log "expected sha256 (pinned): $CORE_SHA"
else
	api_release_json MetaCubeX/mihomo "$MIHOMO_VERSION" "$TMP/core-release.json"
	CORE_SHA="$(asset_digest "$TMP/core-release.json" "$ASSET")"
	is_sha256 "$CORE_SHA" || die "the GitHub API reports no sha256 digest for $ASSET in MetaCubeX/mihomo $MIHOMO_VERSION; refusing to download it unverified"
	log "expected sha256 (GitHub API): $CORE_SHA"
fi

ARCHIVE="$CACHE_DIR/$ASSET"
fetch_verified "$CORE_URL" "$ARCHIVE" "$CORE_SHA"
log "verified: $ASSET"

case "$ASSET_EXT" in
gz)
	gunzip -c "$ARCHIVE" >"$TMP/$OUT_NAME"
	;;
zip)
	command -v unzip >/dev/null 2>&1 || die "unzip is required to extract $ASSET"
	unzip -q -o "$ARCHIVE" -d "$TMP/unzip"
	found="$(find "$TMP/unzip" -type f -name 'mihomo*.exe' | head -n 1)"
	[ -n "$found" ] || die "no mihomo*.exe inside $ASSET"
	mv "$found" "$TMP/$OUT_NAME"
	;;
esac
[ -s "$TMP/$OUT_NAME" ] || die "extracted core is empty"
chmod 0755 "$TMP/$OUT_NAME"

IS_HOST=0
if [ "$GOOS" = "$(host_goos)" ] && [ "$GOARCH" = "$(host_goarch)" ]; then IS_HOST=1; fi

# The archive is verified; running the binary only checks that the version
# inside matches the tag.
if [ "$IS_HOST" = 1 ]; then
	ver_out="$("$TMP/$OUT_NAME" -v 2>&1 | head -n 1 || true)"
	case "$ver_out" in
	*"Mihomo Meta ${MIHOMO_VERSION}"*) log "runs: $ver_out" ;;
	*) die "downloaded core did not report ${MIHOMO_VERSION}: ${ver_out:-<no output>}" ;;
	esac
fi

mkdir -p "$PLAT_DIR"
mv -f "$TMP/$OUT_NAME" "$PLAT_DIR/$OUT_NAME"
printf '%s  %s\n' "$(sha256 "$PLAT_DIR/$OUT_NAME")" "bin/$GOOS-$GOARCH/$OUT_NAME"
if [ "$IS_HOST" = 1 ]; then
	cp -f "$PLAT_DIR/$OUT_NAME" "$BIN_DIR/$OUT_NAME"
fi

# --- geodata ------------------------------------------------------------
geo_override() {
	case "$1" in
	geoip.metadb) echo "${GEOIP_SHA256:-}" ;;
	geosite.dat) echo "${GEOSITE_SHA256:-}" ;;
	GeoLite2-ASN.mmdb) echo "${ASN_SHA256:-}" ;;
	esac
}

if [ "$SKIP_GEO" != "1" ]; then
	geo_json=""
	for f in "${GEO_FILES[@]}"; do
		want="$(geo_override "$f")"
		if [ -n "$want" ]; then
			is_sha256 "$want" || die "invalid SHA-256 override for $f: $want"
			log "expected sha256 of $f (override): $want"
		else
			if [ -z "$geo_json" ]; then
				geo_json="$TMP/geo-release.json"
				api_release_json MetaCubeX/meta-rules-dat "$GEO_TAG" "$geo_json"
			fi
			want="$(asset_digest "$geo_json" "$f")"
			is_sha256 "$want" || die "the GitHub API reports no sha256 digest for $f in MetaCubeX/meta-rules-dat $GEO_TAG"
			log "expected sha256 of $f (GitHub API): $want"
		fi
		fetch_verified "$GEO_BASE/$f" "$GEO_DIR/$f" "$want"
		printf '%s  %s\n' "$want" "bin/geo/$f"
	done
fi

log "done: $PLAT_DIR/$OUT_NAME (Mihomo $MIHOMO_VERSION, $GOOS/$GOARCH)"

#!/usr/bin/env bash
# Runs the real TUN test inside a Parallels macOS VM (never on the host):
# installs the privileged helper as root via `prlctl exec` (no admin prompt),
# runs TestLiveTUN as the logged-in guest user, then uninstalls the helper
# and checks that nothing is left behind.
#
# Requirements: the VM is running, a user is logged in, Parallels Tools are
# installed in the guest, and build/darwin-arm64/Lanflow.app exists
# (scripts/prepare-resources.sh && go tool mygo build).
#
# Note: `prlctl exec` joins its arguments into one string that the guest's
# shell parses again, so every guest command below is passed as ONE string.
#
# Usage: scripts/vm-tun-test.sh "<vm name>"
set -euo pipefail
cd "$(dirname "$0")/.."
VM="${1:?usage: $0 <vm name>}"
APP="build/darwin-arm64/Lanflow.app"
[ -d "$APP" ] || { echo "missing $APP" >&2; exit 1; }

as_root() { prlctl exec "$VM" "$1"; }
as_user() { prlctl exec "$VM" --current-user "$1"; }

STAGE="build/vmtest"
SERVE="build/vmserve"
rm -rf "$STAGE" "$SERVE" build/vmtest.tgz && mkdir -p "$STAGE" "$SERVE"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test -c -o "$STAGE/app.test" ./internal/app
ditto "$APP" "$STAGE/Lanflow.app"
tar -C "$STAGE" -czf "$SERVE/vmtest.tgz" Lanflow.app app.test

# Hand the files to the guest over the VM's host-only bridge with a short-lived
# HTTP server that serves only the archive (Parallels shared folders can hang
# on macOS guests).
HOST_IP="$(as_root "/usr/sbin/netstat -rn -f inet" | awk '$1=="default"{print $2; exit}' | tr -d '\r')"
PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("",0));print(s.getsockname()[1])')"
python3 -m http.server "$PORT" --bind "$HOST_IP" --directory "$SERVE" >/dev/null 2>&1 &
srv=$!
trap 'kill $srv 2>/dev/null || true; rm -rf "$SERVE"' EXIT
sleep 1

guest_user="$(as_user "/usr/bin/id -un" | tr -d '\r')"
guest_uid="$(as_user "/usr/bin/id -u" | tr -d '\r')"
T="/Users/$guest_user/LanflowTest"
RES="$T/Lanflow.app/Contents/Resources"
echo "guest user: $guest_user (uid $guest_uid), files from http://$HOST_IP:$PORT"

as_user "set -e; rm -rf '$T'; mkdir -p '$T/run/data'; chmod 700 '$T/run/data'; /usr/bin/curl -fsS --noproxy '*' -o '$T/vmtest.tgz' 'http://$HOST_IP:$PORT/vmtest.tgz'; /usr/bin/tar -C '$T' -xzf '$T/vmtest.tgz'; rm '$T/vmtest.tgz'; xattr -dr com.apple.quarantine '$T' 2>/dev/null || true; ls '$RES'"
kill $srv 2>/dev/null || true

# The guest keeps its default DNS (the Parallels gateway on the local
# subnet, which never enters the TUN): Lanflow must point the system DNS at
# a TUN-captured resolver itself and put it back. The original is restored
# on exit as a safety net.
SVC="$(as_root "/usr/sbin/networksetup -listallnetworkservices" | tr -d '\r' | sed -n '2p')"
OLD_DNS="$(as_root "/usr/sbin/networksetup -getdnsservers '$SVC'" | tr -d '\r' | grep -E '^[0-9a-fA-F:.]+$' | tr '\n' ' ' || true)"
restore_dns() { as_root "/usr/sbin/networksetup -setdnsservers '$SVC' ${OLD_DNS:-Empty}; dscacheutil -flushcache" || true; }
trap 'kill $srv 2>/dev/null || true; rm -rf "$SERVE"; restore_dns' EXIT
echo "guest DNS ($SVC) before: ${OLD_DNS:-automatic}"

echo "== routes before"
as_root "/usr/sbin/netstat -rn -f inet | head -8"

echo "== installing the helper as root"
as_root "'$RES/lanflow-helper' install --core '$RES/mihomo' --data-dir '$T/run/data' --uid $guest_uid"
as_root "/bin/ls -l /var/run/lanflow-helper.sock /Library/PrivilegedHelperTools/ /Library/LaunchDaemons/ | grep -i lanflow"

echo "== running TestLiveTUN as $guest_user"
status=0
as_user "LANFLOW_LIVE_TUN=1 LANFLOW_TUN_DATA='$T/run' LANFLOW_TUN_RES='$RES' '$T/app.test' -test.run LiveTUN -test.v -test.count=1" || status=$?

echo "== uninstalling the helper"
as_root "'$RES/lanflow-helper' uninstall" || status=$?
left="$(as_root "ls /Library/PrivilegedHelperTools/com.lanflow.* /Library/LaunchDaemons/com.lanflow.helper.plist /var/run/lanflow-helper.sock 2>/dev/null; pgrep -lf 'com.lanflow|mihomo' 2>/dev/null; true" | tr -d '\r')"
if [ -n "$left" ]; then echo "LEFT BEHIND:"; echo "$left"; status=1; else echo "helper fully removed"; fi
echo "== routes after"
as_root "/usr/sbin/netstat -rn -f inet | head -8"
echo "guest DNS after: $(as_root "/usr/sbin/networksetup -getdnsservers '$SVC'" | tr -d '\r' | head -1)"
restore_dns
trap 'kill $srv 2>/dev/null || true; rm -rf "$SERVE"' EXIT
exit $status

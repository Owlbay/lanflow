#!/usr/bin/env bash
# vm-e2e.sh - the full end-to-end run inside a Parallels macOS VM.
#
# What it does, in order (everything is undone by the EXIT trap):
#   1. builds app.test, starts the test proxy fleet + echo server on the
#      HOST bound only to the VM's gateway address (scripts/vm-e2e-fleet.sh)
#   2. records the VM's state, quits the user's own Lanflow (SIGTERM, which
#      puts the system proxy / DNS back) and moves its data directory aside
#   3. ships Lanflow.app + app.test + the fleet subscription to the guest
#      over a short-lived HTTP server, sets the guest DNS to a known
#      resolver, turns the guest's IPv6 off and installs the privileged
#      helper for the test data dir
#   4. runs the live tests (internal/app/live_e2e_test.go) as root: the VM's
#      macOS refuses local-network connections from non-root processes that
#      are not platform binaries (EHOSTUNREACH), and the fleet is on the
#      local network; the tests capture with tcpdump themselves
#   4b. turns the guest's IPv6 back on (automatic: the virtual network hands
#      out a ULA with NAT66) for the IPv6 leak scenario only
#      (TestLiveLeaksIPv6: with TUN on no IPv6 packet may leave en0), then
#      off again for the rest of the run
#   5. removes the test helper and runs the core-switch test (which must not
#      meet a helper: UseCore would ask for an administrator)
#   6. installs the helper for the user's data dir, seeds it with the fleet
#      subscription, launches the real Lanflow.app (TUN on, so its core is
#      the helper-run root process), samples RSS/CPU idle and under load,
#      quits it and checks system proxy / routes / DNS came back
#   7. teardown: helper reinstalled as the user had it, DNS / IPv6 restored,
#      the user's data put back, their Lanflow relaunched and verified
#   8. writes build/vm-e2e-report.md (pass/fail per item, evidence, resources)
#
# Never touches the host's system proxy / DNS / TUN. The fleet's egress is
# the host's normal network. `prlctl exec` joins its arguments into one
# string the guest shell parses, so every guest command is ONE string.
#
# Requirements: VM running with a user logged in and Parallels Tools, the
# user's Lanflow installed at /Applications/Lanflow.app,
# build/darwin-arm64/Lanflow.app built (scripts/prepare-resources.sh && go
# tool mygo build), bin/mihomo, python3 and openssl on the host.
#
# Usage: scripts/vm-e2e.sh "<vm name>"
#   E2E_GO_FLAGS   extra flags for `go test -c` (e.g. -overlay while another
#                  test file in the package is broken)
#   E2E_TESTS      -test.run pattern (default: the three live e2e tests)
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
VM="${1:?usage: $0 <vm name>}"
APP="build/darwin-arm64/Lanflow.app"
[ -d "$APP" ] || { echo "missing $APP (scripts/prepare-resources.sh && go tool mygo build)" >&2; exit 1; }
[ -x bin/mihomo ] || { echo "missing bin/mihomo (scripts/fetch-core.sh)" >&2; exit 1; }
prlctl list "$VM" 2>/dev/null | grep -q running || { echo "VM $VM is not running" >&2; exit 1; }

as_root() { prlctl exec "$VM" "$1"; }
as_user() { prlctl exec "$VM" --current-user "$1"; }
cr() { tr -d '\r'; }
now() { date '+%H:%M:%S'; }
log() { echo "[$(now)] $*"; }

WORK="build/vm-e2e"
FLEET="$WORK/fleet"
SERVE="$WORK/serve"
STAGE="$WORK/stage"
OUT="$WORK/out"
REPORT="build/vm-e2e-report.md"
rm -rf "$WORK" && mkdir -p "$FLEET" "$SERVE" "$STAGE" "$OUT"
ITEMS="$OUT/items.txt"   # E2E|item|status|evidence lines (tests + script)
: >"$ITEMS"
item() { printf 'E2E|%s|%s|%s\n' "$1" "$2" "$(echo "$3" | cr | tr '\n' ' ')" | tee -a "$ITEMS"; }
STATUS=0
fail() { STATUS=1; log "FAIL: $*"; }

# ---- host: gateway address, build, fleet -------------------------------
HOST_IP="$(as_root "/usr/sbin/netstat -rn -f inet" | awk '$1=="default"{print $2; exit}' | cr)"
[ -n "$HOST_IP" ] || { echo "cannot find the VM's default gateway" >&2; exit 1; }
log "VM gateway (fleet address): $HOST_IP"

build_test() {
  for attempt in 1 2 3 4 5 6; do
    # shellcheck disable=SC2086
    if GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go test ${E2E_GO_FLAGS:-} -c -o "$STAGE/app.test" ./internal/app 2>"$WORK/build.err"; then return 0; fi
    log "go test -c failed (attempt $attempt; another agent may be mid-edit), retrying in 30s"; sed -n 1,5p "$WORK/build.err"
    sleep 30
  done
  cat "$WORK/build.err" >&2
  return 1
}
build_test || exit 1
ditto "$APP" "$STAGE/Lanflow.app"

# Teardown state (set as we go; the trap undoes only what was done).
FLEET_UP=0 SRV_PID="" USER_APP_STOPPED=0 USER_DATA_MOVED=0 HELPER_TEST=0 DNS_SET=0 V6_SET=0 LOAD_PID="" UD_SEEDED=0
USER_HOME="" UD="" SVC="" OLD_DNS="" OLD_V6="" HELPER_CFG=""
T=""

restore_user() {
  # Order matters: our helper install replaced the user's; the user's app
  # must find its helper, data, DNS and IPv6 as they were.
  [ -n "$LOAD_PID" ] && kill "$LOAD_PID" 2>/dev/null
  as_root "pkill -x app.test; pkill -f '$T/Lanflow.app/Contents/MacOS/Lanflow'; true" >/dev/null 2>&1
  as_root "/usr/sbin/networksetup -setnetworkserviceenabled '$SVC' on" >/dev/null 2>&1
  if [ "$HELPER_TEST" = 1 ]; then
    log "teardown: removing the test helper"
    as_root "'$T/Lanflow.app/Contents/Resources/lanflow-helper' uninstall" >/dev/null 2>&1 || true
    as_root "pkill -9 -x com.lanflow.mihomo; true" >/dev/null 2>&1
    HELPER_TEST=0
  fi
  if [ "$V6_SET" = 1 ]; then
    case "$OLD_V6" in
      Off) ;;
      "Link-local only") as_root "/usr/sbin/networksetup -setv6LinkLocal '$SVC'" >/dev/null 2>&1 ;;
      *) as_root "/usr/sbin/networksetup -setv6automatic '$SVC'" >/dev/null 2>&1 ;;
    esac
    V6_SET=0
  fi
  if [ "$DNS_SET" = 1 ]; then as_root "/usr/sbin/networksetup -setdnsservers '$SVC' ${OLD_DNS:-Empty}; dscacheutil -flushcache" >/dev/null 2>&1; DNS_SET=0; fi
  if [ "$UD_SEEDED" = 1 ]; then as_user "rm -rf '$UD'" >/dev/null 2>&1; UD_SEEDED=0; fi
  if [ "$USER_DATA_MOVED" = 1 ]; then
    log "teardown: putting the user's data back"
    as_user "rm -rf '$UD'; mv '$UD.e2e-backup' '$UD'" && USER_DATA_MOVED=0
  fi
  if [ -n "$HELPER_CFG" ]; then
    log "teardown: reinstalling the user's helper"
    local core data uid
    core="$(printf '%s' "$HELPER_CFG" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["coreSource"])')"
    data="$(printf '%s' "$HELPER_CFG" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["dataDir"])')"
    uid="$(printf '%s' "$HELPER_CFG" | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["uid"])')"
    as_root "'/Applications/Lanflow.app/Contents/Resources/lanflow-helper' install --core '$core' --data-dir '$data' --uid $uid" >/dev/null 2>&1 || log "helper reinstall failed"
    HELPER_CFG=""
  fi
  [ -n "$T" ] && as_root "rm -rf '$T'" >/dev/null 2>&1
  if [ "$USER_APP_STOPPED" = 1 ]; then
    log "teardown: relaunching the user's Lanflow"
    as_user "open /Applications/Lanflow.app" >/dev/null 2>&1 || log "open failed"
    USER_APP_STOPPED=0
  fi
}
cleanup() {
  set +e
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  restore_user
  if [ "$FLEET_UP" = 1 ]; then scripts/vm-e2e-fleet.sh down "$FLEET"; FLEET_UP=0; fi
}
trap cleanup EXIT

scripts/vm-e2e-fleet.sh up "$HOST_IP" "$FLEET" || exit 1
FLEET_UP=1
host_listeners() { lsof -nP "-i@$HOST_IP" 2>/dev/null | awk 'NR>1 {print $1, $8, $9}' | sort -u | tr '\n' ';'; }

# ---- guest: state before, quit the user's Lanflow ----------------------
guest_user="$(as_user "/usr/bin/id -un" | cr)"
guest_uid="$(as_user "/usr/bin/id -u" | cr)"
USER_HOME="/Users/$guest_user"
UD="$USER_HOME/Library/Application Support/Lanflow"
T="$USER_HOME/LanflowTest"
RES="$T/Lanflow.app/Contents/Resources"
SVC="$(as_root "/usr/sbin/networksetup -listallnetworkservices" | cr | sed -n '2p')"
VM_IP="$(as_root "/sbin/ifconfig en0" | cr | awk '$1=="inet"{print $2; exit}')"
log "guest: $guest_user (uid $guest_uid), $VM_IP on $SVC"

state() { # a one-line summary of what the user cares about
  as_root "echo proc=\$(pgrep -x Lanflow | wc -l | tr -d ' ')/core=\$(pgrep -x com.lanflow.mihomo | wc -l | tr -d ' ')/usercore=\$(pgrep -x mihomo | wc -l | tr -d ' '); /usr/sbin/networksetup -getwebproxy '$SVC' | tr '\n' ' '; echo; echo dns=\$(/usr/sbin/networksetup -getdnsservers '$SVC' | tr '\n' ' '); echo tun=\$(/usr/sbin/netstat -rn -f inet | awk '\$4 ~ /^utun/ {print \$4}' | sort -u | tr '\n' ' '); echo default=\$(/usr/sbin/netstat -rn -f inet | awk '\$1==\"default\"{print \$2\"@\"\$4}' | tr '\n' ' '); echo ipv6=\$(/usr/sbin/networksetup -getinfo '$SVC' | awk -F': ' '\$1==\"IPv6\"{print \$2}')" | cr
}
BEFORE="$(state)"
log "state before:"; echo "$BEFORE"
HELPER_CFG="$(as_root "cat '/Library/Application Support/Lanflow Helper/config.json' 2>/dev/null" | cr)"
[ -n "$HELPER_CFG" ] && log "user's helper config: $(echo "$HELPER_CFG" | tr -d '\n')"
# (no python in the guest: a bare macOS only has the Xcode stub)
USER_OPEN_AT_LOGIN=1
as_user "grep -q '\"OpenAtLogin\": *false' '$UD/settings.json'" >/dev/null 2>&1 && USER_OPEN_AT_LOGIN=0

if as_user "pgrep -x Lanflow" >/dev/null 2>&1; then
  log "quitting the user's Lanflow (SIGTERM)"
  USER_APP_STOPPED=1
  as_user "pkill -x Lanflow; true" >/dev/null
  for _ in $(seq 1 30); do as_user "pgrep -x Lanflow" >/dev/null 2>&1 || break; sleep 1; done
  sleep 2
fi
AFTER_QUIT="$(state)"
log "state after quit:"; echo "$AFTER_QUIT"
if echo "$AFTER_QUIT" | grep -q "proc=0/core=0" && echo "$AFTER_QUIT" | grep -q "Enabled: No" && ! echo "$AFTER_QUIT" | grep -q "tun=utun"; then
  item "user-app/quit-restores-system" PASS "after SIGTERM: $(echo "$AFTER_QUIT" | tr '\n' ' ')"
else
  item "user-app/quit-restores-system" FAIL "after SIGTERM: $(echo "$AFTER_QUIT" | tr '\n' ' ')"; fail "user's app did not clean up"
fi
OLD_DNS="$(as_root "/usr/sbin/networksetup -getdnsservers '$SVC'" | cr | grep -E '^[0-9a-fA-F:.]+$' | tr '\n' ' ' || true)"
OLD_V6="$(echo "$AFTER_QUIT" | sed -n 's/^ipv6=//p')"
log "guest DNS before tests: ${OLD_DNS:-automatic}; IPv6: $OLD_V6"

# Move the user's data aside (a leftover backup from an aborted run wins).
if as_user "test -d '$UD.e2e-backup'" >/dev/null 2>&1; then
  log "found a backup from an earlier run; discarding the current data dir"
  as_user "rm -rf '$UD'" >/dev/null
elif as_user "test -d '$UD'" >/dev/null 2>&1; then
  as_user "mv '$UD' '$UD.e2e-backup'" >/dev/null || exit 1
fi
USER_DATA_MOVED=1

# ---- transfer ----------------------------------------------------------
mkdir -p "$STAGE/fleet" && cp "$FLEET/sub.yaml" "$FLEET/ports" "$FLEET/secret" "$STAGE/fleet/"
tar -C "$STAGE" -czf "$SERVE/vmtest.tgz" Lanflow.app app.test fleet
PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("",0));print(s.getsockname()[1])')"
python3 -m http.server "$PORT" --bind "$HOST_IP" --directory "$SERVE" >/dev/null 2>&1 &
SRV_PID=$!
sleep 1
as_root "set -e; rm -rf '$T'; mkdir -p '$T/run/data' '$T/out'; chmod 700 '$T/run' '$T/run/data'; chmod 755 '$T' '$T/out'; /usr/bin/curl -fsS --noproxy '*' -o '$T/vmtest.tgz' 'http://$HOST_IP:$PORT/vmtest.tgz'; /usr/bin/tar -C '$T' -xzf '$T/vmtest.tgz'; rm '$T/vmtest.tgz'; chmod -R a+rX '$T/Lanflow.app' '$T/fleet' '$T/app.test'; xattr -dr com.apple.quarantine '$T' 2>/dev/null || true; ls '$RES' '$T/fleet'" | cr || exit 1
kill "$SRV_PID" 2>/dev/null; SRV_PID=""

# ---- guest prep: DNS, IPv6 off, helper -----------------------------------
TEST_DNS="114.114.114.114"
as_root "/usr/sbin/networksetup -setdnsservers '$SVC' $TEST_DNS; dscacheutil -flushcache" >/dev/null && DNS_SET=1
# IPv6 off for the audit: the virtual network hands out a ULA with NAT66,
# which lets IPv6 slip past a v4-only TUN (reported separately).
as_root "/usr/sbin/networksetup -setv6off '$SVC'" >/dev/null && V6_SET=1
log "installing the helper for $T/run/data (uid 0)"
if as_root "'$RES/lanflow-helper' install --core '$RES/mihomo' --data-dir '$T/run/data' --uid 0" | cr; then
  HELPER_TEST=1; TUN=1
  item "helper/install" PASS "$(as_root "ls -l /var/run/lanflow-helper.sock /Library/PrivilegedHelperTools/ | grep -i lanflow" | cr | tr '\n' ' ')"
else
  TUN=0; item "helper/install" FAIL "install failed; TUN parts skipped"; fail "helper install"
fi
LISTEN_BEFORE="$(host_listeners)"

# ---- the live tests (as root, see the header) ----------------------------
ENV="LANFLOW_LIVE_E2E=1 LANFLOW_E2E_DATA='$T/run' LANFLOW_E2E_RES='$RES' LANFLOW_E2E_FLEET='$T/fleet' LANFLOW_E2E_HOST=$HOST_IP LANFLOW_E2E_TUN=$TUN LANFLOW_E2E_IFACE=en0 LANFLOW_E2E_OUT='$T/out' LANFLOW_E2E_SYSDNS=$TEST_DNS LANFLOW_E2E_SVC='$SVC' LANFLOW_E2E_V6OFF=$V6_SET HOME=/var/root"
TESTS="${E2E_TESTS:-LiveProxyMatrix|LiveLeaks\$|LiveRobustness}"  # LiveLeaksIPv6 runs in its own phase below
log "running $TESTS in the VM"
as_root "cd '$T' && $ENV '$T/app.test' -test.run '$TESTS' -test.v -test.count=1 -test.timeout 40m" | cr | tee "$OUT/tests.log" | grep -E '^(E2E\||--- |PASS|FAIL|ok|panic)' || true
grep '^E2E|' "$OUT/tests.log" >>"$ITEMS" || true
grep -qE '^(PASS|ok)' "$OUT/tests.log" || { fail "live tests did not pass"; }
grep -E '^--- (PASS|FAIL|SKIP)' "$OUT/tests.log" | sed 's/^/  /'

# ---- IPv6 leak scenario: guest IPv6 on, the TUN must take it ------------
# The virtual network offers IPv6 (ULA + NAT66), which a v4-only TUN lets
# through. IPv6 goes back to what the user had only at teardown (V6_SET).
V6_SCENARIO=0
if [ "$TUN" = 1 ]; then
  log "IPv6 scenario: turning the guest's IPv6 on (automatic)"
  as_root "/usr/sbin/networksetup -setv6automatic '$SVC'" >/dev/null
  v6addr=""
  for _ in $(seq 1 30); do
    v6addr="$(as_root "/sbin/ifconfig en0" | cr | awk '$1=="inet6" && $2 !~ /^fe80/ {print $2; exit}')"
    [ -n "$v6addr" ] && break
    sleep 1
  done
  sleep 2
  v6route="$(as_root "/usr/sbin/netstat -rn -f inet6" | cr | awk '$1=="default" && $3 !~ /I/ {print $4; exit}')"
  log "guest IPv6: addr=${v6addr:-none} default route via ${v6route:-none}"
  item "leak/ipv6-on/guest-network" INFO "en0 inet6 ${v6addr:-none}; IPv6 default route via ${v6route:-none}"
  if [ -n "$v6addr" ]; then
    V6_SCENARIO=1
    as_root "cd '$T' && $ENV LANFLOW_E2E_V6OFF=0 '$T/app.test' -test.run 'LiveLeaksIPv6' -test.v -test.count=1 -test.timeout 15m" | cr | tee "$OUT/ipv6.log" | grep -E '^(E2E\||--- |PASS|FAIL|ok|panic)' || true
    grep '^E2E|' "$OUT/ipv6.log" >>"$ITEMS" || true
    grep -qE '^(PASS|ok)' "$OUT/ipv6.log" || fail "IPv6 leak scenario did not pass"
  else
    item "leak/ipv6-on/guest-network" FAIL "the guest got no IPv6 address within 30s of enabling it; scenario skipped"; fail "guest IPv6"
  fi
  as_root "/usr/sbin/networksetup -setv6off '$SVC'" >/dev/null
  log "IPv6 scenario done; guest IPv6 off again for the rest of the run"
fi
LISTEN_AFTER="$(host_listeners)"

# Core switching needs the helper gone (UseCore would otherwise register the
# new binary through an administrator prompt), so the test helper goes now.
if [ "$HELPER_TEST" = 1 ]; then
  log "removing the test helper"
  as_root "'$RES/lanflow-helper' uninstall" | cr
  as_root "pkill -9 -x com.lanflow.mihomo; true" >/dev/null 2>&1
  HELPER_TEST=0
  left="$(as_root "ls /Library/PrivilegedHelperTools/com.lanflow.* /Library/LaunchDaemons/com.lanflow.helper.plist /var/run/lanflow-helper.sock 2>/dev/null; pgrep -lf 'com.lanflow' 2>/dev/null; true" | cr)"
  [ -z "$left" ] && item "helper/uninstall" PASS "nothing left behind" || item "helper/uninstall" FAIL "left behind: $left"
fi
# The "bundled" core is looked up next to the executable: give app.test one.
as_root "ln -sf '$RES/mihomo' '$T/mihomo'" >/dev/null
log "running LiveCoreSwitch in the VM (no helper)"
as_root "cd '$T' && $ENV LANFLOW_E2E_TUN=0 '$T/app.test' -test.run 'LiveCoreSwitch' -test.v -test.count=1 -test.timeout 10m" | cr | tee "$OUT/coreswitch.log" | grep -E '^(E2E\||--- |PASS|FAIL|ok|panic)' || true
grep '^E2E|' "$OUT/coreswitch.log" >>"$ITEMS" || true
grep -qE '^(PASS|ok)' "$OUT/coreswitch.log" || fail "core switch test did not pass"

# Pull the captures' text form for the report.
CAPTURES="tun-global sysproxy ipv6-global-ipv6-off ipv6-rule-ipv6-off ipv6-rule-ipv6-on"
for f in $CAPTURES; do
  as_root "cat '$T/out/$f.txt' 2>/dev/null" | cr >"$OUT/$f.txt" || true
  [ -s "$OUT/$f.txt" ] && log "capture $f: $(wc -l <"$OUT/$f.txt" | tr -d ' ') packets"
done

# ---- resource baseline with the real app (TUN on: helper-run core) -------
log "helper for the user's data dir, seeding it, launching the real app"
as_user "mkdir -p '$UD'; chmod 700 '$UD'" >/dev/null && UD_SEEDED=1
if as_root "'$RES/lanflow-helper' install --core '$RES/mihomo' --data-dir '$UD' --uid $guest_uid" >/dev/null 2>&1; then HELPER_TEST=1; else item "real-app/helper" FAIL "helper install for $UD failed"; fail "real-app helper"; fi
SEED_ENV="LANFLOW_LIVE_E2E=1 LANFLOW_E2E_DATA='$T/run' LANFLOW_E2E_RES='$RES' LANFLOW_E2E_FLEET='$T/fleet' LANFLOW_E2E_HOST=$HOST_IP LANFLOW_E2E_OUT='$T/out' LANFLOW_E2E_SEED=1 LANFLOW_E2E_SEED_TUN=$HELPER_TEST LANFLOW_E2E_SEED_OPENATLOGIN=$USER_OPEN_AT_LOGIN"
if as_user "cd '$T' && $SEED_ENV '$T/app.test' -test.run LiveSeedRealApp -test.v -test.count=1" | cr | tee "$OUT/seed.log" | grep -qE '^PASS'; then
  grep '^E2E|' "$OUT/seed.log" >>"$ITEMS" || true
  as_user "open '$T/Lanflow.app'" >/dev/null
  up=0
  for _ in $(seq 1 45); do
    st="$(state)"
    if echo "$st" | grep -q "Enabled: Yes" && echo "$st" | grep -q "tun=utun" && echo "$st" | grep -q "core=1"; then up=1; break; fi
    sleep 1
  done
  if [ "$up" = 1 ]; then
    item "real-app/launch-auto-system-proxy-and-tun" PASS "$(echo "$st" | tr '\n' ' ')"
    sleep 8  # let startup settle
    sample() { # label count
      for _ in $(seq 1 "$2"); do
        as_root "ps -axo rss=,pcpu=,comm= | grep -E '(/Lanflow|/mihomo|/com.lanflow.mihomo)\$' | grep -v grep" | cr | sed "s|^|$1 |" >>"$OUT/res.txt"
        sleep 2
      done
    }
    sample idle 8
    log "load: 3 x 96 MiB downloads + 300 small requests through the app's mixed port"
    as_user "for i in 1 2 3; do /usr/bin/curl -s -o /dev/null -m 180 -x http://127.0.0.1:7890 'http://$HOST_IP:40000/blob?mb=96' & done; i=0; while [ \$i -lt 300 ]; do /usr/bin/curl -s -o /dev/null -m 10 -x http://127.0.0.1:7890 'http://$HOST_IP:40000/'; i=\$((i+1)); done; wait" >/dev/null 2>&1 &
    LOAD_PID=$!
    sleep 2
    sample load 8
    wait "$LOAD_PID" 2>/dev/null; LOAD_PID=""
    sample after 3
    r="$(as_user "/usr/bin/curl -s -m 10 -x http://127.0.0.1:7890 'http://$HOST_IP:40000/'" | cr)"
    echo "$r" | grep -q "\"ip\": \"$HOST_IP\"" && item "real-app/request-via-fleet" PASS "echo via the real app's port 7890: $r" || item "real-app/request-via-fleet" FAIL "echo: $r"
    as_user "pkill -x Lanflow; true" >/dev/null
    for _ in $(seq 1 30); do as_user "pgrep -x Lanflow" >/dev/null 2>&1 || break; sleep 1; done
    sleep 3
    q="$(state)"
    if echo "$q" | grep -q "Enabled: No" && echo "$q" | grep -q "core=0" && ! echo "$q" | grep -q "tun=utun" && echo "$q" | grep -q "dns=$TEST_DNS"; then
      item "real-app/quit-restores-proxy-routes-dns" PASS "after SIGTERM: $(echo "$q" | tr '\n' ' ')"
    else
      item "real-app/quit-restores-proxy-routes-dns" FAIL "after SIGTERM: $(echo "$q" | tr '\n' ' ')"; fail "real app quit"
    fi
  else
    item "real-app/launch-auto-system-proxy-and-tun" FAIL "the real app did not come up (proxy+TUN+core) within 45s: $(echo "$st" | tr '\n' ' ')"; fail "real app launch"
    as_user "pkill -x Lanflow; true" >/dev/null
  fi
else
  item "seed/real-app-data" FAIL "see $OUT/seed.log"; fail "seed"
fi

# ---- teardown + verify the user's setup is back ------------------------
restore_user
log "waiting for the user's Lanflow to come back"
AFTER=""
for _ in $(seq 1 60); do
  AFTER="$(state)"
  if echo "$AFTER" | grep -q "proc=1/core=1" && echo "$AFTER" | grep -q "Enabled: Yes" && echo "$AFTER" | grep -q "tun=utun"; then break; fi
  sleep 2
done
log "state after:"; echo "$AFTER"
norm() { echo "$1" | sed -E 's/utun[0-9]+/utunN/g'; }
if [ "$(norm "$AFTER")" = "$(norm "$BEFORE")" ]; then
  item "restore/user-lanflow-back" PASS "state after == state before: $(echo "$AFTER" | tr '\n' ' ')"
else
  item "restore/user-lanflow-back" FAIL "before: $(echo "$BEFORE" | tr '\n' ' ') || after: $(echo "$AFTER" | tr '\n' ' ')"; fail "restore"
fi
HELPER_AFTER="$(as_root "cat '/Library/Application Support/Lanflow Helper/config.json' 2>/dev/null" | cr | tr -d '\n')"
left="$(as_root "pgrep -lf 'LanflowTest' 2>/dev/null; true" | cr)"
[ -z "$left" ] && item "restore/no-test-processes" PASS "nothing from $T running" || item "restore/no-test-processes" FAIL "$left"
if echo "$HELPER_AFTER" | grep -q "\"dataDir\": *\"$UD\"" && echo "$HELPER_AFTER" | grep -q '"coreSource": *"/Applications/Lanflow.app/Contents/Resources/mihomo"'; then
  item "restore/helper-config" PASS "$HELPER_AFTER"
else
  item "restore/helper-config" FAIL "$HELPER_AFTER"; fail "helper config"
fi

# ---- report ------------------------------------------------------------
{
  echo "# VM end-to-end report"
  echo
  echo "- date: $(date '+%Y-%m-%d %H:%M %Z')"
  echo "- VM: $VM (macOS $(as_root "sw_vers -productVersion" | cr)), guest $guest_user, $VM_IP; fleet/echo at $HOST_IP (host bridge only)"
  echo "- app: $APP; core: $(bin/mihomo -v | head -1); helper: $("$APP/Contents/Resources/lanflow-helper" version)"
  echo "- fleet protocols: $(grep -v '^echo=\|^controller=' "$FLEET/ports" | cut -d= -f1 | tr '\n' ' ')"
  echo "- overall: $([ "$STATUS" = 0 ] && echo PASS || echo FAIL) ($(grep -c '|PASS|' "$ITEMS") pass, $(grep -c '|FAIL|' "$ITEMS") fail, $(grep -c '|WARN|' "$ITEMS") warn, $(grep -c '|INFO|' "$ITEMS") info)"
  echo
  echo "## Items"
  echo
  echo "| Item | Result | Evidence |"
  echo "|---|---|---|"
  while IFS='|' read -r _ it st ev; do
    printf '| %s | %s | %s |\n' "$it" "$st" "$(echo "$ev" | sed 's/|/\\|/g')"
  done <"$ITEMS"
  echo
  echo "## Resource baseline (real Lanflow.app with the fleet subscription, TUN on)"
  echo
  if [ -s "$OUT/res.txt" ]; then
    echo "RSS in MiB, CPU in % (ps sampling every 2 s; load = 3 concurrent 96 MiB downloads + 300 small requests through port 7890). mihomo here is the helper-run core (com.lanflow.mihomo)."
    echo
    echo "| Phase | Process | Samples | RSS avg | RSS max | CPU avg | CPU max |"
    echo "|---|---|---|---|---|---|---|"
    awk '{ n=split($4,p,"/"); key=$1" "p[n]; c[key]++; rss[key]+=$2; if($2>rmax[key])rmax[key]=$2; cpu[key]+=$3; if($3>cmax[key])cmax[key]=$3 }
         END { for (k in c) { split(k,a," "); printf "| %s | %s | %d | %.1f | %.1f | %.1f | %.1f |\n", a[1], a[2], c[k], rss[k]/c[k]/1024, rmax[k]/1024, cpu[k]/c[k], cmax[k] } }' "$OUT/res.txt" | sort
  else
    echo "not measured (see items)"
  fi
  echo
  echo "## Captures"
  echo
  for f in $CAPTURES; do
    if [ -s "$OUT/$f.txt" ]; then
      echo "- $f: $(wc -l <"$OUT/$f.txt" | tr -d ' ') packets on en0, $(awk '$2=="IP6"' "$OUT/$f.txt" | wc -l | tr -d ' ') of them IPv6 ($OUT/$f.txt); first lines:"
      echo
      echo '```'
      head -12 "$OUT/$f.txt"
      echo '```'
    fi
  done
  echo
  echo "## State"
  echo
  echo '```'
  echo "before:"; echo "$BEFORE"; echo; echo "after the user's app quit:"; echo "$AFTER_QUIT"; echo; echo "after restore:"; echo "$AFTER"
  echo; echo "host sockets on $HOST_IP before tests: $LISTEN_BEFORE"; echo "host sockets on $HOST_IP after tests:  $LISTEN_AFTER"
  echo '```'
  echo
  echo "## Environment notes"
  echo
  echo "- The guest (macOS $(as_root "sw_vers -productVersion" | cr)) refuses local-network connections from processes that are neither root nor Apple platform binaries when they are started through Parallels Tools (connect: no route to host). The tests therefore run as root and the real app runs with TUN on (helper-run core). A user-run core of a GUI app gets the Local Network permission prompt instead."
  echo "- The host runs its own proxy client with TUN, so every DNS answer the VM gets from a public resolver is a fake IP of the host (198.18.0.0/15). The tests use IP literals for the fleet and classify such traffic as \`fakeip-direct\`."
  echo "- LAN destinations (the VM's own subnet, including the echo by IP) never enter a TUN, so the TUN checks use \`echo.fleet.test\`, a name only the fleet resolves."
  echo "- Guest DNS was set to $TEST_DNS and IPv6 turned off for the tests (on again, automatic, for the IPv6 scenario only: ran=$V6_SCENARIO), then put back (DNS ${OLD_DNS:-automatic}, IPv6 $OLD_V6); the user's Lanflow sets its own DNS while it runs."
  echo "- Test logs: $OUT/tests.log, $OUT/coreswitch.log, $OUT/seed.log; raw pcaps stay in the guest at $T/out."
} >"$REPORT"
log "report: $REPORT"
exit $STATUS

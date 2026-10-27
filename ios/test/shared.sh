#!/usr/bin/env bash
# Share three sessions against the local ushd that test/with-ushd.sh started,
# then run a command with USH_TEST_PORT and USH_TEST_SEED set:
#
#   test/with-ushd.sh ios/test/shared.sh swift test --package-path ios/USHKit
#
# swift-echo and swift-other echo lines back as got:LINE; swift-ender does the
# same but exits on "quit", ending its session. swift-mouse turns on SGR mouse
# tracking and writes the raw bytes it receives to $USH_TEST_MOUSE.
set -euo pipefail
unset USH_SESSION

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
TMP=$(mktemp -d)
PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; rm -rf "$TMP"; }
trap cleanup EXIT

SEED=$(sed -n 's/^USH_SEED=//p' "$ROOT/.env")
make -s -C "$ROOT" build >/dev/null
USH_RC=$TMP/ushrc USH_SEED=$SEED "$ROOT/bin/ush" init -y --seed --profile local \
  --rendezvous 127.0.0.1 --port "$USH_TEST_PORT" --key "$TMP/psk" --skip-host-key-check >/dev/null

share() { # name script
  mkfifo "$TMP/$1.in"
  USH_RC=$TMP/ushrc "$ROOT/bin/ush" share -n "$1" -- sh -c "$2" \
    <"$TMP/$1.in" >"$TMP/$1.out" 2>&1 &
  PIDS+=($!)
  exec {fd}>"$TMP/$1.in" # hold the fifo open so the sharer never sees EOF
}
ECHO='while IFS= read -r l; do printf "got:%s\n" "$l"; done'
share swift-echo "$ECHO"
share swift-other "$ECHO"
share swift-ender 'while IFS= read -r l; do [ "$l" = quit ] && exit 0; printf "got:%s\n" "$l"; done'
export USH_TEST_MOUSE=$TMP/mouse.bytes
share swift-mouse "stty raw -echo; printf '\\033[?1000h\\033[?1006h'; exec cat -u >'$USH_TEST_MOUSE'"

SSH=(ssh -i "$TMP/psk" -p "$USH_TEST_PORT" -o StrictHostKeyChecking=no
     -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR ush@127.0.0.1)
n=0
for _ in $(seq 30); do
  n=$("${SSH[@]}" list 2>/dev/null | grep -c '"name": "swift-' || true)
  [[ $n -ge 4 ]] && break
  sleep 0.5
done
[[ $n -ge 4 ]] || { cat "$TMP"/*.out; echo "sharers never registered" >&2; exit 1; }

USH_TEST_SEED=$SEED "$@"

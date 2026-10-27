#!/usr/bin/env bash
# End-to-end test: share a session from this machine through the containerised
# rendezvous, find it in the registry, join it, and prove bytes flow both ways.
set -euo pipefail

# These checks share terminals, and ush refuses to share from inside a shared
# one. Run them as if from a plain shell, whatever started this script.
unset USH_SESSION

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$ROOT/test/tmp
KEY=$TMP/psk
RC=$TMP/ushrc
SSH_OPTS=(-i "$KEY" -p 7874 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)

pass() { echo "  ok   $*"; }
fail() { echo "  FAIL $*" >&2; exit 1; }

cleanup() {
  local p
  [[ -n ${SSH_AGENT_PID:-} ]] && ssh-agent -k >/dev/null 2>&1
  for p in ${HOST_PID:-} ${HOST2_PID:-} ${KEEP_PID:-}; do kill "$p" 2>/dev/null || true; done
  tmux kill-session -t =e2e-keeper 2>/dev/null || true
  exec 3>&- 2>/dev/null || true
  exec 4>&- 2>/dev/null || true
  rm -f "$TMP/hostin" "$TMP/hostin2"
  [[ ${KEEP_UP:-0} == 1 ]] || docker compose -f "$ROOT/docker-compose.yml" down -t 2 >/dev/null 2>&1
}
trap cleanup EXIT

mkdir -p "$TMP"
rm -f "$RC" "$KEY" "$KEY.pub"

echo "==> building the client"
(cd "$ROOT" && go build -o bin/ush ./cmd/ush) || fail "client build failed"
pass "client built"

echo "==> setting up the client from the seed, as a user would"
# The rendezvous runs with the seed in .env; the client derives the same key.
make -s -C "$ROOT" seed >/dev/null
USH_SEED=$(sed -n 's/^USH_SEED=//p' "$ROOT/.env")
export USH_RC=$RC
USH_SEED=$USH_SEED "$ROOT/bin/ush" init -y --seed --profile local --rendezvous 127.0.0.1 \
  --port 7874 --key "$KEY" --skip-host-key-check >/dev/null \
  || fail "ush init --seed failed"
# Same relay, but nothing listens on this ssh port.
"$ROOT/bin/ush" init -y --profile broken --rendezvous 127.0.0.1 \
  --port 1 --key "$KEY" --skip-host-key-check >/dev/null \
  || fail "ush init for a second profile failed"
unset USH_RC
# A friendly machine name, deliberately with a space and an apostrophe.
MACHINE="Alex's e2e box"
{ printf 'machine = "%s"\n' "$MACHINE"; cat "$RC"; } > "$RC.new" && mv "$RC.new" "$RC"
[[ -f $KEY && -f $KEY.pub ]] || fail "ush init --seed did not write the key"
pass "ush init --seed derived the key and wrote both profiles"

echo "==> running with an SSH agent that holds an unrelated key"
# Like a real workstation. A key helper that prefers agent keys over the
# configured key file would get every share rejected.
rm -f "$TMP/agent.sock" "$TMP/unrelated_key" "$TMP/unrelated_key.pub"
ssh-keygen -q -t ed25519 -N '' -f "$TMP/unrelated_key"
eval "$(ssh-agent -s -a "$TMP/agent.sock")" >/dev/null
ssh-add -q "$TMP/unrelated_key"
pass "agent running with an unrelated key"

echo "==> building and starting the rendezvous"
docker compose -f "$ROOT/docker-compose.yml" up -d --build >/dev/null
for _ in $(seq 1 30); do
  ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list >/dev/null 2>&1 && break
  sleep 1
done
ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list >/dev/null || fail "rendezvous never came up"
pass "rendezvous reachable, forced command answers 'list'"
set +e
NOTTY=$(timeout 10 ssh -T "${SSH_OPTS[@]}" ush@127.0.0.1 </dev/null 2>&1)
NOTTY_RC=$?
set -e
if [[ $NOTTY_RC -eq 0 || $NOTTY_RC -eq 124 ]] || ! grep -q "needs a terminal" <<<"$NOTTY"; then
  fail "menu without a terminal should refuse quickly (rc=$NOTTY_RC): $NOTTY"
fi
pass "menu without a terminal refuses at once instead of spinning"

echo "==> ush ping"
USH_RC=$RC "$ROOT/bin/ush" ping > "$TMP/ping.out" 2>&1 || { cat "$TMP/ping.out"; fail "ush ping failed against a healthy rendezvous"; }
grep -q "^rendezvous .* ok" "$TMP/ping.out" || { cat "$TMP/ping.out"; fail "ush ping did not report the rendezvous ok"; }
pass "ush ping: healthy rendezvous passes"
if USH_RC=$RC "$ROOT/bin/ush" ping -s >/dev/null 2>&1; then fail "ush ping -s should be refused: there is one path now"; fi
pass "ush ping -s is refused: one port, one path"
set +e
USH_RC=$RC "$ROOT/bin/ush" --profile broken ping > "$TMP/ping-broken.out" 2>&1
PING_RC=$?
set -e
if [[ $PING_RC -eq 0 ]] || ! grep -q "^rendezvous.*FAILED: unreachable" "$TMP/ping-broken.out"; then
  cat "$TMP/ping-broken.out"; fail "ush ping should report the dead rendezvous as unreachable"
fi
pass "ush ping: dead port reported as unreachable, exit $PING_RC"

echo "==> sharing a session"
# stdin that never reaches EOF, so the shared command stays alive the way an
# interactive session would (the command gets its own pty).
rm -f "$TMP/hostin"
mkfifo "$TMP/hostin"
# shellcheck disable=SC2016  # the loop body is meant to run on the far side
(cd "$ROOT" && USH_RC=$RC exec "$ROOT/bin/ush" share -n e2e -d "end to end test" -- \
  sh -c 'while IFS= read -r l; do printf "got:%s\n" "$l"; done' \
  < "$TMP/hostin" > "$TMP/host.out" 2>&1) &
HOST_PID=$!
exec 3> "$TMP/hostin"

echo "==> waiting for it to appear in the registry"
for _ in $(seq 1 30); do
  LIST=$(ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list 2>/dev/null || echo '[]')
  MINE=$(jq -c '[.[] | select(.name == "e2e")] | sort_by(.started_epoch) | last // empty' <<<"$LIST")
  [[ -n $MINE ]] && break
  sleep 1
done
[[ -n ${MINE:-} ]] || { cat "$TMP/host.out"; fail "session never registered"; }
pass "session registered: $(jq -c '{name, machine, description}' <<<"$MINE")"

SID=$(jq -r '.session_id' <<<"$MINE")
[[ $(jq -r '.description' <<<"$MINE") == "end to end test" ]] || fail "description lost"
pass "name and description survived the round trip"
[[ $(jq -r '.command' <<<"$MINE") == sh\ -c\ * ]] || fail "command is $(jq -r '.command' <<<"$MINE"), not the shared command"
pass "menu shows the real command, not a wrapper"
[[ $(jq -r '.cwd' <<<"$MINE") == "$ROOT" ]] || fail "cwd is $(jq -r '.cwd' <<<"$MINE"), expected $ROOT"
pass "working directory recorded"
[[ $(jq -r '.machine' <<<"$MINE") == "$MACHINE" ]] || fail "machine is $(jq -r '.machine' <<<"$MINE"), expected the configured name"
pass "friendly machine name shown instead of the hostname"

if grep -q 'SSH:\|SFTP:\|Session:' "$TMP/host.out"; then
  cat "$TMP/host.out"; fail "something other than the session was printed on the sharer's terminal"
fi
pass "nothing but the session on the sharer's terminal"

echo "==> joining it"
# -tt keeps the connection open after stdin closes, exactly as a real client
# would, so the read has to be bounded.
JOINED=$({ printf 'hello\n'; sleep 4; } | timeout 15 ssh -tt "${SSH_OPTS[@]}" ush@127.0.0.1 "join $SID" 2>&1 || true)
grep -q 'got:hello' <<<"$JOINED" || { echo "--- join output ---"; echo "$JOINED"; fail "no echo back from the session"; }
pass "typed into the session and got its output back"

echo "==> checking deregistration"
kill "$HOST_PID" 2>/dev/null || true
HOST_PID=
for _ in $(seq 1 20); do
  LIST=$(ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list 2>/dev/null || echo '[]')
  STILL=$(jq -r --arg sid "$SID" '[.[] | select(.session_id == $sid)] | length' <<<"$LIST")
  [[ $STILL -eq 0 ]] && break
  sleep 1
done
[[ $STILL -eq 0 ]] || fail "entry outlived the session"
pass "entry disappeared when the session died"

echo "==> sharing without a description"
# An empty field must survive the trip: it is optional on the command line.
rm -f "$TMP/hostin2"
mkfifo "$TMP/hostin2"
# shellcheck disable=SC2016
USH_RC=$RC "$ROOT/bin/ush" share -n nodesc -- \
  sh -c 'while IFS= read -r l; do printf "got:%s\n" "$l"; done' \
  < "$TMP/hostin2" > "$TMP/host2.out" 2>&1 &
HOST2_PID=$!
exec 4> "$TMP/hostin2"

for _ in $(seq 1 20); do
  LIST=$(ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list 2>/dev/null || echo '[]')
  MINE2=$(jq -c '[.[] | select(.name == "nodesc")] | last // empty' <<<"$LIST")
  [[ -n $MINE2 ]] && break
  sleep 1
done
[[ -n ${MINE2:-} ]] || { cat "$TMP/host2.out"; fail "session with no description never registered"; }
[[ $(jq -r '.description' <<<"$MINE2") == "" ]] || fail "empty description came back as $(jq -r '.description' <<<"$MINE2")"
pass "registers fine with no description"

echo "==> the shared command knows it is being shared"
rm -f "$TMP/envseen"
# shellcheck disable=SC2016  # expanded by the shared command, not here
USH_RC=$RC timeout 30 "$ROOT/bin/ush" share -n envcheck -- sh -c 'printf %s "$USH_SESSION" > "$0"' "$TMP/envseen" </dev/null >/dev/null 2>&1 || true
[[ $(cat "$TMP/envseen" 2>/dev/null) == envcheck ]] || fail "USH_SESSION was '$(cat "$TMP/envseen" 2>/dev/null)', want envcheck"
pass "USH_SESSION=envcheck inside the shared command"

echo "==> ush refuses to nest inside a shared terminal"
nest_refused() {  # $1: a description, rest: the arguments to ush
  local what=$1; shift
  local out
  out=$(USH_RC=$RC USH_SESSION=envcheck timeout 10 "$ROOT/bin/ush" "$@" 2>&1 </dev/null) && \
    fail "$what should refuse to run inside a shared session"
  grep -q "already inside ush session" <<<"$out" || fail "$what nesting message was: $out"
}
nest_refused "ush" 
nest_refused "ush share" share -- /bin/true
# The subcommands that do not take over the terminal still work in there.
USH_RC=$RC USH_SESSION=envcheck timeout 20 "$ROOT/bin/ush" ping >/dev/null 2>&1 || \
  fail "ush ping should still work inside a shared session"
pass "ush and ush share refuse to nest; ping still runs"

echo "==> SIGKILL cannot leave a phantom entry"
# No trap runs on SIGKILL; the kernel closing the socket is what deregisters.
SID2=$(jq -r '.session_id' <<<"$MINE2")
kill -9 "$HOST2_PID" 2>/dev/null || true
HOST2_PID=
for _ in $(seq 1 20); do
  LIST=$(ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list 2>/dev/null || echo '[]')
  STILL=$(jq -r --arg sid "$SID2" '[.[] | select(.session_id == $sid)] | length' <<<"$LIST")
  [[ $STILL -eq 0 ]] && break
  sleep 1
done
[[ $STILL -eq 0 ]] || fail "entry outlived a SIGKILLed client"
pass "entry disappeared after SIGKILL"

echo "==> -k --mux tmux keeps the command alive across a rendezvous restart"
tmux kill-session -t =e2e-keeper 2>/dev/null || true
USH_RC=$RC "$ROOT/bin/ush" share -k --mux tmux -n e2e-keeper -- sleep 600 </dev/null > "$TMP/keep.out" 2>&1 &
KEEP_PID=$!
keeper_sids() { ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list 2>/dev/null | jq -r '.[] | select(.name == "e2e-keeper") | .session_id'; }
FIRST_SID=""
for _ in $(seq 1 20); do FIRST_SID=$(keeper_sids); [[ -n $FIRST_SID ]] && break; sleep 1; done
[[ -n $FIRST_SID ]] || { cat "$TMP/keep.out"; fail "kept session never registered"; }
docker compose -f "$ROOT/docker-compose.yml" restart rendezvous >/dev/null 2>&1
for _ in $(seq 1 30); do ssh "${SSH_OPTS[@]}" ush@127.0.0.1 list >/dev/null 2>&1 && break; sleep 1; done
kill -0 "$KEEP_PID" 2>/dev/null || { cat "$TMP/keep.out"; fail "ush share -k exited when the rendezvous restarted"; }
tmux has-session -t =e2e-keeper 2>/dev/null || fail "the tmux session died with the rendezvous"
SIDS=""
for _ in $(seq 1 45); do SIDS=$(keeper_sids); [[ -n $SIDS && $SIDS != "$FIRST_SID" ]] && break; sleep 1; done
[[ -n $SIDS && $SIDS != "$FIRST_SID" ]] || { cat "$TMP/keep.out"; fail "did not re-register after the restart"; }
[[ $(wc -l <<<"$SIDS") -eq 1 ]] || fail "expected one e2e-keeper entry, got: $SIDS"
pass "survived a rendezvous restart and re-registered once, as a new session"
tmux kill-session -t =e2e-keeper
for _ in $(seq 1 10); do kill -0 "$KEEP_PID" 2>/dev/null || break; sleep 1; done
if kill -0 "$KEEP_PID" 2>/dev/null; then fail "ush share -k kept running after its tmux session ended"; fi
set +e; wait "$KEEP_PID"; KEEP_RC=$?; set -e
KEEP_PID=
[[ $KEEP_RC -eq 0 ]] || { cat "$TMP/keep.out"; fail "ush share -k exited $KEEP_RC when its session ended, want 0"; }
pass "stops cleanly (exit 0) once the tmux session ends"
set +e
KBAD=$(USH_RC=$RC "$ROOT/bin/ush" share -K -n e2e-keeper -- top 2>&1); KBAD_RC=$?
set -e
if [[ $KBAD_RC -ne 2 ]]; then
  fail "the old -K should be refused, not share anything: exit $KBAD_RC, $KBAD"
fi
pass "the old -K is refused; -k takes a command now"

echo "==> an unreachable rendezvous fails fast and loudly (via --profile)"
set +e
ERR=$(USH_RC=$RC timeout 30 "$ROOT/bin/ush" share --profile broken -n unreachable -- true 2>&1 </dev/null)
RC_CODE=$?
set -e
[[ $RC_CODE -ne 0 && $RC_CODE -ne 124 ]] || fail "expected a prompt non-zero exit, got $RC_CODE: $ERR"
grep -q "rendezvous" <<<"$ERR" || fail "error does not mention the rendezvous: $ERR"
pass "exits $RC_CODE with: $(head -1 <<<"$ERR")"

echo; echo "all checks passed"

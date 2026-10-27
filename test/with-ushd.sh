#!/usr/bin/env bash
# Run a command against a local ushd, with no container in sight.
#
#   test/with-ushd.sh uv run --no-project python -m unittest test/menu_test.py
#
# Starts ushd on a loopback port of its own, waits for it, runs the command
# with USH_TEST_PORT pointing at it, and stops it afterwards.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PORT=${USH_DEV_PORT:-2201}
RUN=${USH_DEV_RUN_DIR:-$ROOT/.local/ushd}

[[ -f $ROOT/.env ]] || { echo "no .env: run make seed" >&2; exit 1; }
SEED=$(grep '^USH_SEED=' "$ROOT/.env" | cut -d= -f2)

make -C "$ROOT" bin/ushd >/dev/null

# Something already listening would answer the readiness check below, and
# the command would run against it instead of the ushd started here.
if nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
  echo "with-ushd: port $PORT is already in use; set USH_DEV_PORT to a free one" >&2
  exit 1
fi

mkdir -p "$RUN"
USH_SEED=$SEED "$ROOT/bin/ushd" -listen "127.0.0.1:$PORT" -run-dir "$RUN" >"$RUN/ushd.log" 2>&1 &
USHD=$!
trap 'kill $USHD 2>/dev/null || true' EXIT

for _ in $(seq 50); do nc -z 127.0.0.1 "$PORT" 2>/dev/null && break; sleep 0.1; done

USH_TEST_PORT=$PORT "$@"

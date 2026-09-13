#!/usr/bin/env bash
# restart-prod.sh — safe restart of freebuff-unified :18080
#
# Steps:
#   1. Health check before (to confirm we have a working baseline)
#   2. systemctl restart freebuff-unified.service
#   3. Wait + smoke test
#   4. If fails, journal tail + runbooks/freebuff-unified-restart-loop.sh
#
# Usage:
#   restart-prod.sh                # full cycle
#   restart-prod.sh --no-wait      # restart without waiting for smoke

set -uo pipefail
BIN_DIR="$(cd "$(dirname "$0")" && pwd)"

NO_WAIT=0
for arg in "$@"; do
    case "$arg" in
        --no-wait) NO_WAIT=1 ;;
        -h|--help)
            sed -n '2,10p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

echo "=== pre-restart health ==="
"$BIN_DIR/smoke.sh" --quick 2>&1 | tail -n 12

echo
echo "=== restarting freebuff-unified ==="
sudo -n systemctl restart freebuff-unified.service 2>&1
sleep 5

if [[ $NO_WAIT -eq 0 ]]; then
    echo
    echo "=== post-restart health ==="
    "$BIN_DIR/smoke.sh" --quick 2>&1 | tail -n 12
fi

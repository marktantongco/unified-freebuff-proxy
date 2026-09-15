#!/usr/bin/env bash
# DESC: Rotate freebuff-proxy ADMIN_TOKEN (manual run; timer does this weekly)
#
# Runs the trefeon repo's rotate-admin-token.sh: login -> CSRF ->
# POST /admin/api/change-password. Self-verifies (new token 302, old
# rejected, .env persisted) and exits non-zero on any failure.
#
# The scheduled copy runs via:
#   systemd: freebuff-admin-token-rotation.timer (weekly, Mon 04:17 +jitter)
#   check:   systemctl list-timers freebuff-admin-token-rotation.timer
#
# The new token is NOT printed anywhere. To learn it after a rotation:
#   sudo grep '^ADMIN_TOKEN=' /home/x3/aiworkspace/trefeon-freebuff-proxy/.env
# (use it once to log in at http://127.0.0.1:3457/admin — loopback only)
#
# Manual run:
#   /home/x3/freebuff-unified/scripts/operations/runbook.sh admin-token-rotation
#   # or, to choose the value yourself (>=16 chars, URL-safe):
#   TOKEN_FILE=/dev/shm/mytoken /home/x3/aiworkspace/trefeon-freebuff-proxy/scripts/rotate-admin-token.sh

set -uo pipefail

REPO=/home/x3/aiworkspace/trefeon-freebuff-proxy
ROTATE="$REPO/scripts/rotate-admin-token.sh"

[[ -x "$ROTATE" ]] || { echo "missing $ROTATE" >&2; exit 1; }

echo "=== rotating freebuff-proxy ADMIN_TOKEN (value never printed) ==="
"$ROTATE"

echo "=== post-rotation sanity: chain health ==="
curl -sf -o /dev/null -w 'trefeon /healthz: %{http_code}\n' --max-time 5 http://127.0.0.1:3457/healthz \
    || { echo "WARN: trefeon healthz failed after rotation" >&2; exit 2; }
curl -s -o /dev/null -w 'unified gate: %{http_code} (401 expected)\n' --max-time 5 http://127.0.0.1:18080/v1/models

echo "OK: rotation complete. Retrieve the token with:"
echo "  sudo grep '^ADMIN_TOKEN=' $REPO/.env"

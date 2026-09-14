#!/usr/bin/env bash
# DESC: Gateway returns 5xx for all requests — provider pool dead
# SYMPTOMS:
#   - /healthz returns ok but /v1/chat/completions always errors
#   - /stealth/status shows upstream_unavailable everywhere
#   - All Zen provider probes return 401 (revoked) or 429 (quota)
# STEPS:
#   1. health.sh to confirm state
#   2. secret-audit.sh to check Zen key rotations
#   3. opencode-failover should have auto-rotated to x3 or owl
#   4. If nothing alive: smoke.sh --quick to identify broken layer

set -uo pipefail
BIN_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== Step 1: full health dump ==="
"$BIN_DIR/health.sh"

echo
echo "=== Step 2: secret audit (which provider keys are alive?) ==="
"$BIN_DIR/secret-audit.sh" --keys-only

echo
echo "=== Step 3: failover status (should have switched off dead providers) ==="
systemctl --user status opencode-failover.service --no-pager 2>&1 | grep -E "Active|MainPID" || echo "down"
echo
echo "Last status file:"
cat ~/.local/share/opencode/failover-status.json 2>/dev/null | python3 -m json.tool | head -n 30

echo
echo "=== Step 4: quick smoke (if 4xx on most = key issue, if 5xx = upstream dead) ==="
"$BIN_DIR/smoke.sh" --quick 2>&1 | tail -n 20

echo
echo "=== Recovery actions (run manually) ==="
echo "1. Wait for Zen quota reset (07:00 UTC daily) — no action"
echo "2. If keys revoked: opencode-audit.sh to remove dead slots"
echo "3. If owl-agent dead: systemctl restart owl-agent.service"
echo "4. If x3 prod dead: /home/x3/freebuff-unified/bin/restart-prod.sh"

#!/usr/bin/env bash
# DESC: opencode CLI returns 401 on every model call
# STEPS:
#   1. Verify API key in agent-env + provider config
#   2. Probe each provider's /v1/models directly
#   3. Verify failover service is up and active model is reachable

set -uo pipefail
BIN_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== Step 1: opencode config + agent-env sanity ==="
"$BIN_DIR/opencode-audit.sh"

echo
echo "=== Step 2: per-provider probe ==="
set -a; . ~/.config/opencode/agent-env; set +a
for url_var in "https://opencode.ai/zen/v1:OPENCODE_ZEN_KEY4" "https://opencode.ai/zen/v1:OPENCODE_GO_UKAJ" "http://127.0.0.1:8080/v1:OWL_LOCAL_KEY" "http://127.0.0.1:18080/v1:FREEBUFF_API_KEY"; do
    url="${url_var%%:*}"
    keyvar="${url_var##*:}"
    key="${!keyvar:-}"
    if [[ -z "$key" ]]; then
        echo "  $url  ($keyvar): key not set"
        continue
    fi
    code=$(curl -s -o /dev/null -w "%{http_code}" -m 5 -H "Authorization: Bearer $key" "$url/models")
    echo "  $url  ($keyvar): HTTP=$code"
done

echo
echo "=== Step 3: failover service + last status ==="
systemctl --user is-active opencode-failver.service 2>&1
cat ~/.local/share/opencode/failover-status.json 2>/dev/null | python3 -m json.tool | head -n 20

echo
echo "=== Recovery actions ==="
echo "1. If only Zen 401s: wait for 07:00 UTC quota reset, OR drop dead Zen slots:"
echo "   - Edit opencode.json: remove opencode, opencode2, opencode3, opencode4, opencode5, opencode_go"
echo "   - Keep owl + x3 (loopback)"
echo
echo "2. If all providers fail: systemctl --user restart opencode-failover.service"
echo
echo "3. If key missing in env: source ~/.config/opencode/agent-env manually"

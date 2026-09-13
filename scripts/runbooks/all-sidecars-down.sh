#!/usr/bin/env bash
# DESC: All sidecar units dead — unified owl-agent-stack down
# STEPS:
#   1. Verify systemd unit states
#   2. Restart missing units in dependency order
#   3. Confirm ports bound

set -uo pipefail

UNITS="hermes-sidecar lmarena-stealth-proxy owl-agent freebuff-unified freebuff-proxy"

echo "=== Step 1: unit states ==="
for u in $UNITS; do
    a=$(systemctl is-active "$u.service" 2>/dev/null || sudo -n systemctl is-active "$u.service" 2>/dev/null)
    printf '  %-30s %s\n' "$u.service" "$a"
done

echo
echo "=== Step 2: restart dead units (in dependency order) ==="
for u in hermes-sidecar lmarena-stealth-proxy owl-agent freebuff-unified freebuff-proxy; do
    state=$(systemctl is-active "$u.service" 2>/dev/null || sudo -n systemctl is-active "$u.service" 2>/dev/null)
    if [[ "$state" != "active" ]]; then
        echo "Restarting $u.service (was $state)..."
        sudo -n systemctl restart "$u.service" 2>&1 | head -n 2
        sleep 3
        new=$(systemctl is-active "$u.service" 2>/dev/null || sudo -n systemctl is-active "$u.service" 2>/dev/null)
        echo "  -> $new"
    else
        echo "  $u.service: ok (skip)"
    fi
done

echo
echo "=== Step 3: verify ports ==="
for port in 18080 3101 3103 60000; do
    pid=$(sudo -n ss -tlnp 2>/dev/null | awk -v p=":$port" '$0 ~ p {for(i=1;i<=NF;i++)if($i~/pid=/){gsub(/[^0-9]/,"",$i);print $i;exit}}')
    if [[ -n "$pid" ]]; then
        printf '  \033[32m✓\033[0m :%-10s pid=%s\n' "$port" "$pid"
    else
        printf '  \033[31m✗\033[0m :%-10s (no listener)\n' "$port"
    fi
done

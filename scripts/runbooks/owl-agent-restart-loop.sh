#!/usr/bin/env bash
# DESC: owl-agent.service restart-loop (active=activating, exit-code spam)
# STEPS:
#   1. Identify loop cause
#   2. Apply fix (supervisor wrapper OR exit-78 patch)

set -uo pipefail
BIN_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== Step 1: loop diagnosis ==="
sudo -n journalctl -u owl-agent.service -n 30 --no-pager 2>&1 | grep -E "code=|errno|Errno|SEGV" | tail -n 10

echo
echo "=== Step 2: EADDRINUSE on :60000 ==="
echo "  Cause: kernel hasn't released socket from killed process"
echo "  Fix:   /home/x3/local/bin/owl-sync --restart (auto-handles)"
echo "         OR manual: systemctl stop ; sleep 5 ; systemctl start"
echo
echo "Manual recovery (if owl-sync fails):"
sudo -n systemctl stop owl-agent.service 2>&1
for i in 1 2 3 4 5; do
    sudo -n pkill -9 -f "owl_server.py" 2>/dev/null
    sleep 2
done
sudo -n systemctl reset-failed owl-agent.service 2>&1
sudo -n systemctl daemon-reload 2>&1
sudo -n systemctl start owl-agent.service 2>&1
sleep 8
sudo -n systemctl status owl-agent.service --no-pager 2>&1 | grep -E "Active|MainPID"

echo
echo "=== Step 3: verify health ==="
sudo -u x1 curl -s -m 5 http://127.0.0.1:60000/health 2>&1 | head -c 200
echo
sudo -u x1 curl -s -m 5 http://127.0.0.1:60000/chameleon/stats 2>&1 | python3 -c "import json,sys;d=json.load(sys.stdin);print('chameleon:',d.get('enabled'))"

echo
echo "=== Step 4: verify supervisor wrapper is in place ==="
ls -la /home/x1/.owl-agent/owl-agent-supervisor.sh 2>&1 | head
sudo -n systemctl cat owl-agent.service 2>&1 | grep -E "ExecStart|RestartPreventExit"

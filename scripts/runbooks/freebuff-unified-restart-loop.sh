#!/usr/bin/env bash
# DESC: freebuff-unified systemd unit restart-loops (active=activating, exit code spam)
# STEPS:
#   1. Identify loop cause from journal (EADDRINUSE, SEGV, missing file)
#   2. Apply targeted fix
#   3. Verify stable active state

set -uo pipefail
BIN_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== Step 1: loop diagnosis ==="
sudo -n journalctl -u freebuff-unified.service -n 30 --no-pager 2>&1 | tail -n 20

echo
echo "=== Step 2a: EADDRINUSE on :18080 ==="
echo "  Cause: previous PID still holds port when systemd starts new one"
echo "  Fix:   systemctl stop freebuff-unified ; sleep 5 ; systemctl start"
sudo -n systemctl stop freebuff-unified.service 2>&1
sleep 5
sudo -n systemctl start freebuff-unified.service 2>&1
sleep 3
sudo -n systemctl status freebuff-unified.service --no-pager 2>&1 | grep -E "Active|MainPID"

echo
echo "=== Step 2b: SEGV on startup ==="
echo "  Cause: missing file, bad config, or venv corruption"
echo "  Fix:   ./bin/freebuff-unified check (dry-run config parse)"
sudo -n -u root /home/x3/freebuff-unified/bin/freebuff-unified check 2>&1 | tail -n 20

echo
echo "=== Step 2c: missing env / wrong key ==="
echo "  Cause: config.yaml drift or evals/ perms"
echo "  Fix:   cp config.example.yaml config.yaml.template ; restore keys from backup"
ls -la /home/x3/freebuff-unified/config.yaml 2>&1
test -w /home/x3/freebuff-unified/evals/ && echo "evals/ writable" || echo "evals/ NOT writable"

echo
echo "=== Step 3: verify ==="
"$BIN_DIR/smoke.sh" --quick 2>&1 | tail -n 15

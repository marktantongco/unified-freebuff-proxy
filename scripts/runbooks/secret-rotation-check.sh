#!/usr/bin/env bash
# DESC: Daily secrets hygiene — show current env vs backups, count keys
# STEPS:
#   1. Print key count + bytes for each env file
#   2. Diff vs most recent backup
#   3. Check for duplicate keys
#   4. Reminder: where to find a backup, when to rotate

set -uo pipefail
BIN_DIR="$(cd "$(dirname "$0")/.." && pwd)"

"$BIN_DIR/secret-audit.sh"
echo
echo "=== Rotation reminders ==="
echo "Zen keys (provider opencode..opencode5): rotate when 401/429 sustained > 24h"
echo "Owl/Cloudflare loopback keys: rotate quarterly"
echo "GitHub PAT: rotate on suspicion or every 90d (see API_KEY_ROTATION_CHECKLIST.md)"
echo "Backup location: ~/.config/opencode/agent-env.bak.<timestamp>"

#!/usr/bin/env bash
# opencode-audit.sh — diff opencode config vs backups, report drift
#
# Usage:
#   opencode-audit.sh                # audit opencode.json + AGENTS.md
#   opencode-audit.sh --verify       # assert all provider keys resolve via env

set -uo pipefail

CFG="$HOME/.config/opencode/opencode.json"
AGENTS="$HOME/.config/opencode/AGENTS.md"

for arg in "$@"; do
    case "$arg" in
        -h|--help)
            sed -n '2,8p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

[[ -f "$CFG" ]] || { echo "missing $CFG" >&2; exit 1; }

# Provider count + per-provider apiKey resolution
python3 - <<PY
import json, os, sys
with open("$CFG") as f: c = json.load(f)
providers = c.get("provider", {})
print(f"providers: {len(providers)}")
for name, body in providers.items():
    opts = body.get("options") or {}
    raw_key = opts.get("apiKey","")
    label = body.get("name","")
    if raw_key.startswith("{env:"):
        var = raw_key[5:-1]
        val = os.environ.get(var, "")
        status = "ok" if val else "MISSING"
        print(f"  {name:12} ({label[:40]}) env={var} status={status}")
    else:
        print(f"  {name:12} ({label[:40]}) literal status={'ok' if raw_key else 'MISSING'}")
PY

echo
echo "backup diff:"
ls -1 "$HOME/.config/opencode"/opencode.json.bak_* 2>/dev/null | tail -n 5 | while read b; do
    bn=$(basename "$b")
    if cmp -s "$CFG" "$b"; then
        echo "  $bn: identical"
    else
        echo "  $bn: differs"
    fi
done

# Failover + watch active?
for svc in opencode-failver owl-watch; do
    a=$(systemctl --user is-active "$svc.service" 2>/dev/null)
    echo "  $svc.service: $a"
done

# Verify active model + active provider match
echo
python3 - <<PY
import json
with open("$CFG") as f: c = json.load(f)
m = c.get("model","")
sm = c.get("small_model","")
print(f"active model: {m}")
print(f"active small_model: {sm}")
for name in ("model", "small_model"):
    val = c.get(name, "")
    if val and "/" in val:
        prov, _ = val.split("/", 1)
        if prov not in (c.get("provider") or {}):
            print(f"  WARN: {name} provider '{prov}' not in config")
PY

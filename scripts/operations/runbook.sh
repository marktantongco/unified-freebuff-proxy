#!/usr/bin/env bash
# runbook.sh — incident-response runbook dispatcher
#
# Usage:
#   runbook.sh list
#   runbook.sh <name>        # runs the runbook for that incident class
#
# Runbooks are bash scripts under runbooks/ named <incident>.sh.
# Add a new one with: cp runbooks/template.sh runbooks/<name>.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RUNBOOK_DIR="$SCRIPT_DIR/runbooks"

cmd="${1:-list}"
shift 2>/dev/null || true

list() {
    printf '\033[1m== Available Runbooks ==\033[0m\n'
    for f in "$RUNBOOK_DIR"/*.sh; do
        [[ -f "$f" ]] || continue
        name=$(basename "$f" .sh)
        desc=$(grep -m1 '^# DESC:' "$f" 2>/dev/null | sed 's/^# DESC:[[:space:]]*//')
        printf '  \033[36m%-30s\033[0m %s\n' "$name" "$desc"
    done
    echo
    echo "Run: runbook.sh <name>"
}

run() {
    local name="$1"
    local rb="$RUNBOOK_DIR/$name.sh"
    if [[ ! -f "$rb" ]]; then
        echo "no runbook: $name" >&2
        list >&2
        exit 1
    fi
    echo "=== running $name ==="
    bash "$rb" "$@"
}

case "$cmd" in
    list|ls|"") list ;;
    -h|--help) list ;;
    *)         run "$cmd" "$@" ;;
esac

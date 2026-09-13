#!/usr/bin/env bash
# rotate-backups.sh — prune old config/agent-env backups, keep N most recent
#
# Usage:
#   rotate-backups.sh              # keep last 3 of each (.bak.* files)
#   rotate-backups.sh --keep 5     # keep last 5
#   rotate-backups.sh --dry-run    # show what would be deleted

set -uo pipefail

KEEP=3
DRY_RUN=0
args=()
for arg in "$@"; do
    case "$arg" in
        --keep)    args+=(--keep) ;;
        --dry-run) DRY_RUN=1 ;;
        -h|--help)
            sed -n '2,8p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *)          args+=("$arg") ;;
    esac
done
prev=""
for arg in "${args[@]:-}"; do
    if [[ "$prev" == "--keep" ]]; then KEEP="$arg"; fi
    prev="$arg"
done

dir="$HOME/.config/opencode"

collect() {
    local prefix="$1"
    ls -1 "$dir"/${prefix}* 2>/dev/null | while IFS= read -r f; do
        [[ -f "$f" ]] || continue
        local ts
        ts=$(stat -c '%Y' "$f")
        echo "$ts|$f"
    done | sort -t'|' -k1 -n -r
}

kept=0
deleted=0
for prefix in "opencode.json.bak" "opencode.jsonc.bak" "agent-env.bak"; do
    count=0
    while IFS='|' read -r ts f; do
        [[ -z "$f" ]] && continue
        count=$((count+1))
        if [[ $count -le $KEEP ]]; then
            kept=$((kept+1))
        else
            if [[ $DRY_RUN -eq 1 ]]; then
                echo "would delete: $f"
            else
                rm -f "$f"
                deleted=$((deleted+1))
            fi
        fi
    done < <(collect "$prefix")
done

echo "kept=$kept deleted=$deleted"

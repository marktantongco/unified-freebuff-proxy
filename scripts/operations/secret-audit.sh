#!/usr/bin/env bash
# secret-audit.sh — diff current agent-env vs backups, report drift
#
# Usage:
#   secret-audit.sh                  # show current vs each backup
#   secret-audit.sh --changed        # only show changed files
#   secret-audit.sh --keys-only      # just var names, not values

set -uo pipefail

ENV_FILE="$HOME/.config/opencode/agent-env"
BACKUP_DIR="$HOME/.config/opencode"

CHANGED_ONLY=0
KEYS_ONLY=0
for arg in "$@"; do
    case "$arg" in
        --changed)    CHANGED_ONLY=1 ;;
        --keys-only)  KEYS_ONLY=1 ;;
        -h|--help)
            sed -n '2,8p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

[[ -f "$ENV_FILE" ]] || { echo "missing $ENV_FILE" >&2; exit 1; }

# Collect backup snapshots (file.bak_*)
mapfile -t backups < <(ls -1 "$BACKUP_DIR"/agent-env.bak.* 2>/dev/null | sort -r)

if [[ $KEYS_ONLY -eq 1 ]]; then
    awk -F'=' '/^[A-Z_]/{print $1}' "$ENV_FILE" | sort -u | grep -v '^$'
    exit 0
fi

printf '\033[1m== secret-audit: %s ==\033[0m\n' "$ENV_FILE"
echo "Total keys: $(awk -F'=' '/^[A-Z_]/{print $1}' "$ENV_FILE" | sort -u | wc -l)"
echo "Total bytes: $(stat -c '%s' "$ENV_FILE")"
echo

printf '\033[1m-- current key names --\033[0m\n'
awk -F'=' '/^[A-Z_]/{print $1}' "$ENV_FILE" | sort -u

if [[ ${#backups[@]} -gt 0 ]]; then
    printf '\n\033[1m-- diff vs backups --\033[0m\n'
    for b in "${backups[@]}"; do
        bn=$(basename "$b")
        if cmp -s "$ENV_FILE" "$b"; then
            [[ $CHANGED_ONLY -eq 0 ]] && echo "  $bn: identical"
        else
            echo "  \033[33m$bn: differs\033[0m"
            [[ $CHANGED_ONLY -eq 1 ]] && diff "$b" "$ENV_FILE" | head -n 20
        fi
    done
fi

# Sanity: no duplicate keys
dups=$(awk -F'=' '/^[A-Z_]/{print $1}' "$ENV_FILE" | sort | uniq -d)
if [[ -n "$dups" ]]; then
    printf '\n\033[1;31mWARN: duplicate keys:\033[0m\n'
    echo "$dups"
fi

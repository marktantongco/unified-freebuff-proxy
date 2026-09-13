#!/usr/bin/env bash
# unified-freebuff-proxy uninstaller
# Removes the binary, the seeded .env (optional), and stops any running instance
# started by the installer. Never touches other freebuff-proxy installs
# (e.g. /opt/freebuff) or your CLI login files.

set -euo pipefail

APP="freebuff-proxy"
BIN="${HOME}/.local/bin/${APP}"
ENV_PATH="${XDG_CONFIG_HOME:-${HOME}/.config}/${APP}/.env"
LOG="${XDG_STATE_HOME:-${HOME}/.local/state}/${APP}.log"

REMOVE_ENV=0
[ "${1:-}" = "--purge-config" ] && REMOVE_ENV=1

echo "==> Stopping running instances started from ${BIN}"
if pgrep -f "${BIN} serve" >/dev/null 2>&1; then
  pkill -f "${BIN} serve" && echo "    stopped" || true
  sleep 1
else
  echo "    none running"
fi

if [ -f "$BIN" ]; then
  rm -f "$BIN" && echo "==> Removed ${BIN}"
else
  echo "==> Binary not found at ${BIN} (nothing to remove)"
fi

if [ "$REMOVE_ENV" -eq 1 ]; then
  if [ -f "$ENV_PATH" ]; then
    rm -f "$ENV_PATH" && echo "==> Removed ${ENV_PATH} (--purge-config)"
  fi
else
  [ -f "$ENV_PATH" ] && echo "==> Config kept: ${ENV_PATH} (use --purge-config to remove)"
fi

[ -f "$LOG" ] && echo "==> Log kept: ${LOG}"

echo "Done."

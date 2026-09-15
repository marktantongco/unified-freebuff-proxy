#!/usr/bin/env bash
# install-omarchy.sh — Omarchy Linux entry point for the unified stack.
#
# Asserts Arch/Omarchy + Hyprland, bootstraps yay if missing, then delegates
# everything to install-unified.sh (single source of truth — no duplicated
# logic here), and finishes with Omarchy desktop extras:
#   - walker desktop entry for the gateway dashboard (:9091)
#   - Hyprland exec-once snippet for the dashboard browser tab (opt-in)
#   - ghostty confirmation (Omarchy default terminal)
#
# Usage:
#   ./scripts/install-omarchy.sh [--yes] [--dry-run] [--no-sudo]
#       [--skip-build] [--verify-only] [--uninstall] [--repo-dir DIR]
#       [--skip-owl] [--skip-opencode] [--no-desktop]
# All flags except --no-desktop pass through to install-unified.sh.
#
# Exit codes: 0 ok, 1 usage/error, 2 verify failed (from unified).

set -euo pipefail

if [ -t 1 ]; then
  B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; X=$'\033[0m'
else
  B=""; G=""; Y=""; R=""; X=""
fi
step() { printf '%s\n' "${B}==>${X} $*"; }
info() { printf '%s\n' "    $*"; }
ok()   { printf '%s\n' "    ${G}OK${X}  $*"; }
warn() { printf '%s\n' "    ${Y}!!${X}  $*"; }
die()  { printf '%s\n' "${R}error:${X} $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIFIED="$SCRIPT_DIR/install-unified.sh"
[ -x "$UNIFIED" ] || die "missing $UNIFIED (clone the full repo)"

PASSTHROUGH=()
NO_DESKTOP=0
NEED_HELP=0
for a in "$@"; do
  case "$a" in
    --no-desktop) NO_DESKTOP=1 ;;
    -h|--help) NEED_HELP=1; PASSTHROUGH+=("$a") ;;
    *) PASSTHROUGH+=("$a") ;;
  esac
done
if [ "$NEED_HELP" -eq 1 ]; then
  sed -n '2,16p' "$0" | sed 's/^# \?//'
  echo "--- unified flags ---"
  "$UNIFIED" --help
  exit 0
fi

# ---------------------------------------------------------------- omarchy gate
step "omarchy gate"
OS_ID="unknown"; OS_LIKE=""
if [ -f /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-unknown}"; OS_LIKE="${ID_LIKE:-}"
fi
case "$OS_ID $OS_LIKE" in
  *arch*) ok "arch family ($OS_ID)" ;;
  *) die "not Arch/Omarchy (found: $OS_ID). Use install-unified.sh directly elsewhere." ;;
esac
OMARCHY=0; [ -d /etc/omarchy ] && OMARCHY=1; have omarchy && OMARCHY=1
[ "$OMARCHY" -eq 1 ] && ok "omarchy detected" || warn "arch but not omarchy — desktop extras still apply"
have Hyprland && ok "hyprland present" || warn "no Hyprland session (headless ok)"

# yay bootstrap (unified assumes an AUR helper exists)
if ! have yay && ! have paru; then
  step "yay bootstrap"
  if [[ " ${PASSTHROUGH[*]} " == *" --dry-run "* ]]; then
    info "[dry] sudo pacman -S --needed --noconfirm base-devel git && build yay-bin from AUR"
  else
    sudo pacman -S --needed --noconfirm base-devel git
    tmp="$(mktemp -d)"; git clone https://aur.archlinux.org/yay-bin.git "$tmp/yay-bin"
    (cd "$tmp/yay-bin" && makepkg -si --noconfirm); rm -rf "$tmp"
    ok "yay installed"
  fi
else
  ok "AUR helper present"
fi

# ------------------------------------------------------- delegate to unified
step "delegate to install-unified.sh"
"$UNIFIED" "${PASSTHROUGH[@]}"
rc=$?
[ "$rc" -ne 0 ] && die "install-unified.sh exited $rc"

# ------------------------------------------------------- omarchy desktop extras
if [ "$NO_DESKTOP" -eq 1 ]; then info "desktop extras skipped"; exit 0; fi
step "omarchy desktop extras"
DESK_DIR="$HOME/.local/share/applications"
run_maybe() { if [[ " ${PASSTHROUGH[*]} " == *" --dry-run "* ]]; then printf '    [dry] %s\n' "$*"; else "$@"; fi; }
run_maybe mkdir -p "$DESK_DIR"
DESK="$DESK_DIR/freebuff-dashboard.desktop"
if [ -f "$DESK" ]; then
  info "keep existing $DESK"
else
  if [[ " ${PASSTHROUGH[*]} " == *" --dry-run "* ]]; then
    printf '    [dry] write %s\n' "$DESK"
  else
    cat > "$DESK" <<'EOF'
[Desktop Entry]
Type=Application
Name=Freebuff Dashboard
Comment=Unified gateway dashboard (:9091)
Exec=xdg-open http://127.0.0.1:9091/dashboard
Icon=network-workgroup
Categories=Network;Monitor;
EOF
    ok "walker entry: Freebuff Dashboard"
  fi
fi
# Hyprland exec-once: commented snippet the user can enable (never auto-edit
# hyprland.conf — window rules are personal).
HYP_SNIP="$HOME/.config/hypr/freebuff-unified.conf.example"
if [ -f "$HYP_SNIP" ]; then
  info "keep existing $HYP_SNIP"
else
  if [[ " ${PASSTHROUGH[*]} " == *" --dry-run "* ]]; then
    printf '    [dry] write %s\n' "$HYP_SNIP"
  else
    run_maybe mkdir -p "$(dirname "$HYP_SNIP")"
    cat > "$HYP_SNIP" <<'EOF'
# freebuff-unified — optional Hyprland snippet.
# To enable: add `source = ~/.config/hypr/freebuff-unified.conf` to hyprland.conf
# (file is .example so it never activates by itself).
exec-once = ghostty --title=freebuff-logs -e journalctl -fu freebuff-unified.service
windowrulev2 = float, title:freebuff-logs
EOF
    ok "hyprland snippet: $HYP_SNIP (opt-in, commented by filename)"
  fi
fi
have ghostty && ok "ghostty present (omarchy default)" || warn "ghostty missing"
step "done. dashboard: walker -> 'Freebuff Dashboard' (http://127.0.0.1:9091/dashboard)"

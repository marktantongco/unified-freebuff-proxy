#!/usr/bin/env bash
# install-omarchy.sh — full-stack installer for the freebuff-unified +
# unified-owl ecosystem, designed for Omarchy Linux (Arch + Hyprland),
# with graceful fallback on other distros.
#
# Installs:
#   1. System deps (go, nodejs, python, git, curl, jq) via pacman/yay,
#      apt on Debian/Ubuntu fallback
#   2. Repos: unified-freebuff-proxy (gateway) + unified-owl (stack)
#   3. Builds: freebuff-unified Go binary; npm install both sidecars
#   4. Seeds: config.yaml + sidecar .env files (never overwrites existing)
#   5. Units: system units (gateway, hermes, lmarena) + user units
#      (opencode-failover, owl-watch)
#   6. Verifies: build check, unit states, endpoint smoke
#
# Usage:
#   ./scripts/install-omarchy.sh [--yes] [--dry-run] [--no-sudo]
#       [--skip-build] [--verify-only] [--uninstall] [--repo-dir DIR]
#
# Flags:
#   --yes         assume yes to prompts
#   --dry-run     print every action, change nothing
#   --no-sudo     user units + builds only (skip system units, pacman needs sudo anyway)
#   --skip-build  repos + units only (binaries already built)
#   --verify-only run build-check + smoke, change nothing
#   --uninstall   stop + disable all units installed here (keeps repos/configs)
#   --repo-dir    gateway checkout dir (default: ~/workspace/freebuff-unified
#                 if present, else ./ if run from repo root, else clone target)
#
# Secrets: never written by this script. config.yaml is seeded from
# config.example.yaml with CHANGE_ME placeholders; fill keys before serve.
# Exit codes: 0 ok, 1 usage/error, 2 verify failed.

set -euo pipefail

# ----------------------------------------------------------------------------
# output helpers (match scripts/install.sh style)
# ----------------------------------------------------------------------------
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

YES=0 DRY=0 NO_SUDO=0 SKIP_BUILD=0 VERIFY_ONLY=0 UNINSTALL=0 REPO_DIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --yes) YES=1; shift ;;
    --dry-run) DRY=1; shift ;;
    --no-sudo) NO_SUDO=1; shift ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --verify-only) VERIFY_ONLY=1; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --repo-dir) REPO_DIR="${2:-}"; shift 2 ;;
    --repo-dir=*) REPO_DIR="${1#*=}"; shift ;;
    -h|--help) sed -n '2,30p' "$0" | sed 's/^# \?//'; exit 0 ;;
    *) die "unknown flag: $1 (see --help)" ;;
  esac
done

run() { if [ "$DRY" -eq 1 ]; then printf '    [dry] %s\n' "$*"; else "$@"; fi; }
confirm() {
  [ "$YES" -eq 1 ] && return 0
  printf '    %s [y/N] ' "$1"; read -r ans
  [ "$ans" = "y" ] || [ "$ans" = "Y" ]
}

# ----------------------------------------------------------------------------
# 0. OS detect
# ----------------------------------------------------------------------------
OS_ID="unknown"; OS_LIKE=""; OMARCHY=0
if [ -f /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-unknown}"; OS_LIKE="${ID_LIKE:-}"
fi
[ -d /etc/omarchy ] && OMARCHY=1
have omarchy && OMARCHY=1
case "$OS_ID $OS_LIKE" in
  *arch*) PKG_FAMILY="arch" ;;
  *debian*|*ubuntu*) PKG_FAMILY="debian" ;;
  *) PKG_FAMILY="unknown" ;;
esac
step "OS: $OS_ID (family: $PKG_FAMILY, omarchy: $OMARCHY, hyprland: $(have Hyprland && echo yes || echo no))"
[ "$PKG_FAMILY" = "unknown" ] && die "unsupported distro: $OS_ID (need Arch/Omarchy or Debian/Ubuntu)"

# ----------------------------------------------------------------------------
# 1. resolve repo dir
# ----------------------------------------------------------------------------
if [ -z "$REPO_DIR" ]; then
  if [ -f ./go.mod ] && grep -q '^module freebuff-unified' ./go.mod 2>/dev/null; then
    REPO_DIR="$PWD"
  elif [ -d "$HOME/workspace/freebuff-unified/.git" ]; then
    REPO_DIR="$HOME/workspace/freebuff-unified"
  elif [ -d /home/x3/freebuff-unified/.git ]; then
    REPO_DIR="/home/x3/freebuff-unified"
  else
    REPO_DIR="$HOME/workspace/freebuff-unified"
  fi
fi
info "repo dir: $REPO_DIR"

if [ "$UNINSTALL" -eq 1 ]; then
  step "uninstall: stop + disable units (repos/configs kept)"
  for u in freebuff-unified hermes-sidecar lmarena-stealth-proxy; do
    run sudo systemctl disable --now "$u.service" || true
  done
  for u in opencode-failover owl-watch; do
    run systemctl --user disable --now "$u.service" || true
  done
  ok "units stopped+disabled"
  exit 0
fi

# ----------------------------------------------------------------------------
# 2. system deps
# ----------------------------------------------------------------------------
step "system deps"
install_arch() {
  local pkgs=(go nodejs npm python python-pip git curl jq base-devel)
  if have yay; then
    run yay -S --needed --noconfirm "${pkgs[@]}"
  elif have paru; then
    run paru -S --needed --noconfirm "${pkgs[@]}"
  else
    warn "no AUR helper (yay/paru); pacman only (base-devel group prompt possible)"
    run sudo pacman -S --needed "${pkgs[@]}"
  fi
  # omarchy extras: walker launcher entry works out of the box; nothing to install.
  # ghostty/kitty already present on omarchy; ensure a terminal exists for logs.
  have ghostty || have kitty || have alacritty || warn "no GPU terminal found (ghostty/kitty/alacritty)"
}
install_debian() {
  run sudo apt-get update
  run sudo apt-get install -y golang nodejs npm python3 python3-pip git curl jq build-essential
}
if [ "$VERIFY_ONLY" -eq 0 ]; then
  if [ "$PKG_FAMILY" = "arch" ]; then install_arch; else install_debian; fi
  for t in node npm python3 git curl jq; do
    have "$t" && ok "$t $( $t --version 2>&1 | head -n1 )" || die "missing after install: $t"
  done
  if have go; then
    ok "go $(go version 2>&1)"
    gover="$(go version 2>&1 | grep -o 'go[0-9]*\.[0-9]*' | tr -d 'go')"
    [ "$gover" = "1.26" ] || warn "go is $gover, repo go.mod wants 1.26 (build may still work)"
  else
    die "missing after install: go"
  fi
fi

# ----------------------------------------------------------------------------
# 3. repos
# ----------------------------------------------------------------------------
step "repos"
clone_or_update() { # url dir
  if [ -d "$2/.git" ]; then
    info "$2 exists, pulling"
    run git -C "$2" pull --ff-only || warn "pull failed in $2 (offline? keeping local)"
  else
    run git clone "$1" "$2"
  fi
}
if [ "$VERIFY_ONLY" -eq 0 ]; then
  if [ ! -d "$REPO_DIR/.git" ]; then
    clone_or_update "https://github.com/marktantongco/unified-freebuff-proxy.git" "$REPO_DIR"
  else
    info "gateway repo present"
  fi
  if [ ! -d "$HOME/workspace/unified-owl/.git" ]; then
    clone_or_update "https://github.com/marktantongco/unified-owl.git" "$HOME/workspace/unified-owl"
  else
    info "unified-owl present"
  fi
  [ -f "$REPO_DIR/go.mod" ] || die "not a gateway checkout: $REPO_DIR"
fi

# ----------------------------------------------------------------------------
# 4. build
# ----------------------------------------------------------------------------
step "build"
if [ "$SKIP_BUILD" -eq 0 ] && [ "$VERIFY_ONLY" -eq 0 ]; then
  run go -C "$REPO_DIR" build -o bin/freebuff-unified ./cmd/freebuff
  ok "gateway binary: $REPO_DIR/bin/freebuff-unified"
  for sc in deps/hermes-service deps/lmarena-stealth-proxy; do
    if [ -f "$REPO_DIR/$sc/package.json" ]; then
      if [ -d "$REPO_DIR/$sc/node_modules" ]; then info "$sc node_modules present"; else
        run npm --prefix "$REPO_DIR/$sc" install --no-audit --no-fund
      fi
    fi
  done
  ok "sidecars installed"
else
  info "build skipped"
fi

# ----------------------------------------------------------------------------
# 5. seed configs (never overwrite)
# ----------------------------------------------------------------------------
step "seed configs"
seed() { # src dst modes
  if [ -f "$2" ]; then info "keep existing $2"; else
    run install -m "$3" "$1" "$2"; ok "seeded $2"
  fi
}
if [ "$VERIFY_ONLY" -eq 0 ]; then
  seed "$REPO_DIR/config.example.yaml" "$REPO_DIR/config.yaml" 600
  for sc in deps/hermes-service deps/lmarena-stealth-proxy; do
    [ -f "$REPO_DIR/$sc/.env.example" ] && seed "$REPO_DIR/$sc/.env.example" "$REPO_DIR/$sc/.env" 600
  done
  # lmarena default port lives in code (3103); ensure .env agrees if we created it
  if [ -f "$REPO_DIR/deps/lmarena-stealth-proxy/.env" ] && ! grep -q '^PORT=' "$REPO_DIR/deps/lmarena-stealth-proxy/.env"; then
    run bash -c "echo PORT=3103 >> '$REPO_DIR/deps/lmarena-stealth-proxy/.env'"
  fi
  mkdir -p "$REPO_DIR/evals"
  ok "evals dir ready (gitignored user data)"
fi

# ----------------------------------------------------------------------------
# 6. units
# ----------------------------------------------------------------------------
step "systemd units"
install_system_unit() { # src name
  local dst="/etc/systemd/system/$2"
  if [ -f "$dst" ] && cmp -s "$1" "$dst"; then info "$2 in place"; return 0; fi
  if [ "$NO_SUDO" -eq 1 ]; then warn "skip $2 (--no-sudo)"; return 0; fi
  run sudo install -m 644 "$1" "$dst"
  ok "installed $2"
}
if [ "$VERIFY_ONLY" -eq 0 ]; then
  # gateway unit: repo ships hermes+lmarena sidecars; gateway unit template lives here
  run mkdir -p "$REPO_DIR/deploy/systemd"
  if [ ! -f "$REPO_DIR/deploy/systemd/freebuff-unified.service" ]; then
    warn "no gateway unit template in repo; writing standard one"
    if [ "$DRY" -eq 0 ]; then
      cat > "$REPO_DIR/deploy/systemd/freebuff-unified.service" <<EOF
[Unit]
Description=Freebuff Unified API Gateway (:18080)
After=network.target hermes-sidecar.service lmarena-stealth-proxy.service
Wants=hermes-sidecar.service lmarena-stealth-proxy.service
[Service]
Type=simple
WorkingDirectory=$REPO_DIR
ExecStart=$REPO_DIR/bin/freebuff-unified -config $REPO_DIR/config.yaml serve
EnvironmentFile=/etc/freebuff-unified/probe-env
Restart=on-failure
RestartSec=5
[Install]
WantedBy=multi-user.target
EOF
    else
      printf '    [dry] write %s\n' "$REPO_DIR/deploy/systemd/freebuff-unified.service"
    fi
  fi
  for u in freebuff-unified hermes-sidecar lmarena-stealth-proxy; do
    install_system_unit "$REPO_DIR/deploy/systemd/$u.service" "$u.service"
  done
  # probe env for /health/all + /readyz bearer injection (read-only key file)
  if [ ! -f /etc/freebuff-unified/probe-env ]; then
    warn "create /etc/freebuff-unified/probe-env with FREEBUFF_API_KEY=<first server key> (chmod 644, health probes only)"
  else
    ok "probe-env present"
  fi
  if [ "$NO_SUDO" -eq 0 ]; then
    run sudo systemctl daemon-reload
    for u in hermes-sidecar lmarena-stealth-proxy freebuff-unified; do
      if confirm "enable+start $u?"; then run sudo systemctl enable --now "$u.service"; else info "skip $u"; fi
    done
  fi
  # user units (opencode-failover, owl-watch live in ~/.local/bin + ~/.config/systemd/user)
  for f in opencode-failover owl-watch; do
    if [ -x "$HOME/.local/bin/$f" ] && [ -f "$HOME/.config/systemd/user/$f.service" ]; then
      if confirm "enable+start user unit $f?"; then
        run systemctl --user daemon-reload
        run systemctl --user enable --now "$f.service"
      fi
    else
      info "user unit $f not present (ships with workstation, not this repo); skip"
    fi
  done
fi

# ----------------------------------------------------------------------------
# 7. verify: check + units + smoke
# ----------------------------------------------------------------------------
step "verify"
if [ ! -x "$REPO_DIR/bin/freebuff-unified" ]; then
  die "binary missing: build first (drop --skip-build)"
fi
run "$REPO_DIR/bin/freebuff-unified" -config "$REPO_DIR/config.yaml" check || die "config check failed"
for u in freebuff-unified hermes-sidecar lmarena-stealth-proxy; do
  if systemctl is-active --quiet "$u.service" 2>/dev/null || sudo systemctl is-active --quiet "$u.service" 2>/dev/null; then
    ok "$u active"
  else
    warn "$u not active"
  fi
done
KEY="$(grep -o 'fbu_[a-z0-9]*' "$REPO_DIR/config.yaml" 2>/dev/null | head -n 1)"
probe() { # path expect
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" -m 10 ${KEY:+-H "Authorization: Bearer $KEY"} "http://127.0.0.1:18080$1" || echo 000)
  [ "$code" = "$2" ] && ok "$1 -> $code" || { warn "$1 -> $code (want $2)"; return 1; }
}
fails=0
probe /healthz 200 || fails=$((fails+1))
probe /readyz 200 || fails=$((fails+1))
probe /health/all 200 || fails=$((fails+1))
probe /v1/models 200 || fails=$((fails+1))
probe /v1/lmarena/evals 200 || fails=$((fails+1))
probe /lmarena/healthz 200 || fails=$((fails+1))
[ "$fails" -eq 0 ] && ok "smoke 6/6" || die "smoke $fails failed (exit 2)"
step "done. fill keys in $REPO_DIR/config.yaml, then: systemctl restart freebuff-unified"

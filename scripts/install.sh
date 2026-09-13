#!/usr/bin/env bash
# unified-freebuff-proxy installer
# Installs the trefeon freebuff-proxy (Go, MIT) as the "unified freebuff proxy":
#   OpenAI /v1/chat/completions + /v1/responses, Anthropic /v1/messages,
#   embedded admin dashboard, token pool, GLM 5.3 Flash default model.
#
# Usage:
#   curl -fsSL <raw-url>/scripts/install.sh | bash
#   ./scripts/install.sh [--prefix DIR] [--port N] [--token TOKEN] [--env-file FILE]
#                        [--no-start] [--yes]
#
# What it does:
#   1. Detects OS/arch, downloads the matching prebuilt release from
#      trefeon/freebuff-proxy GitHub releases (no Go toolchain needed).
#   2. Verifies SHA-256 against the release checksums.txt.
#   3. Installs the binary into a platform-standard path.
#   4. Seeds ~/.config/freebuff-proxy/.env (never overwrites an existing one)
#      with listen addr, upstream, dashboard admin token, and - when present -
#      the CLI login token auto-discovered from
#      ~/.config/{freebuff,manicode,codebuff}/credentials.json (the proxy also
#      does this discovery itself at runtime when AUTH_TOKENS is empty).
#   5. Starts the proxy (unless --no-start) and health-checks /healthz.
#
# Nothing here prints your token to stdout; the .env is chmod 600.

set -euo pipefail

REPO="trefeon/freebuff-proxy"
APP="freebuff-proxy"
GH_API="https://api.github.com/repos/${REPO}/releases/latest"
DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"

# ----------------------------------------------------------------------------
# pretty output helpers
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

# ----------------------------------------------------------------------------
# args
# ----------------------------------------------------------------------------
PREFIX=""
PORT_ARG=""
TOKEN_ARG=""
ENV_FILE_ARG=""
NO_START=0

while [ $# -gt 0 ]; do
  case "$1" in
    --prefix)     PREFIX="${2:-}"; shift 2 ;;
    --prefix=*)   PREFIX="${1#*=}"; shift ;;
    --port)       PORT_ARG="${2:-}"; shift 2 ;;
    --port=*)     PORT_ARG="${1#*=}"; shift ;;
    --token)      TOKEN_ARG="${2:-}"; shift 2 ;;
    --token=*)    TOKEN_ARG="${1#*=}"; shift ;;
    --env-file)   ENV_FILE_ARG="${2:-}"; shift 2 ;;
    --env-file=*) ENV_FILE_ARG="${1#*=}"; shift ;;
    --no-start)   NO_START=1; shift ;;
    -h|--help)    sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

# ----------------------------------------------------------------------------
# platform detection
# ----------------------------------------------------------------------------
OS="$(uname -s)"
ARCH="$(uname -m)"
case "$OS" in
  Linux)  OS_TAG="linux" ;;
  Darwin) OS_TAG="darwin" ;;
  *) die "unsupported OS: $OS (linux/darwin; on Windows use the release ZIP manually)" ;;
esac
case "$ARCH" in
  x86_64|amd64)  ARCH_TAG="amd64" ;;
  aarch64|arm64) ARCH_TAG="arm64" ;;
  *) die "unsupported arch: $ARCH" ;;
esac

# ----------------------------------------------------------------------------
# dependency check
# ----------------------------------------------------------------------------
for dep in curl tar; do
  have "$dep" || die "missing dependency: $dep"
done
SHA_TOOL="sha256sum"
if ! have sha256sum; then
  if have shasum; then SHA_TOOL="shasum -a 256"; else
    warn "no sha256sum/shasum found; checksum verification will be skipped"
    SHA_TOOL=""
  fi
fi

# ----------------------------------------------------------------------------
# locate install dirs (platform-standard, matches upstream docs)
# ----------------------------------------------------------------------------
if [ -n "$PREFIX" ]; then
  BIN_DIR="${PREFIX}/bin"
  CONFIG_DIR="${PREFIX}/config"
  SHARE_DIR="${PREFIX}/share"
else
  case "$OS" in
    Linux)
      BIN_DIR="${HOME}/.local/bin"
      CONFIG_DIR="${XDG_CONFIG_HOME:-${HOME}/.config}/${APP}"
      SHARE_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/${APP}" ;;
    Darwin)
      BIN_DIR="/usr/local/bin"
      CONFIG_DIR="${HOME}/Library/Application Support/${APP}"
      SHARE_DIR="/usr/local/share/${APP}" ;;
  esac
  if [ ! -w "$BIN_DIR" ] && [ ! -w "$(dirname "$BIN_DIR")" ]; then
    if have sudo && sudo -n true 2>/dev/null; then
      SUDO="sudo"
    else
      warn "cannot write to ${BIN_DIR}; falling back to ${HOME}/.local/bin"
      BIN_DIR="${HOME}/.local/bin"
      mkdir -p "$BIN_DIR"
    fi
  fi
fi
mkdir -p "$BIN_DIR"

# ----------------------------------------------------------------------------
# resolve latest version
# ----------------------------------------------------------------------------
step "Resolving latest release version"
VERSION=""
if have python3; then
  VERSION="$(curl -fsSL -m 30 "$GH_API" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("tag_name",""))' 2>/dev/null || true)"
elif have jq; then
  VERSION="$(curl -fsSL -m 30 "$GH_API" | jq -r '.tag_name // empty' 2>/dev/null || true)"
else
  VERSION="$(curl -fsSL -m 30 "$GH_API" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
fi
[ -n "$VERSION" ] || die "could not resolve latest release (network problem or GitHub rate limit). Retry later."
VERSION="${VERSION#v}"
info "latest release: v${VERSION}"

ASSET="${APP}_${VERSION}_${OS_TAG}_${ARCH_TAG}.tar.gz"
ASSET_URL="${DOWNLOAD_BASE}/v${VERSION}/${ASSET}"
CHECKSUMS_URL="${DOWNLOAD_BASE}/v${VERSION}/checksums.txt"

# ----------------------------------------------------------------------------
# download + verify
# ----------------------------------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

step "Downloading ${ASSET}"
curl -fL --retry 3 --retry-delay 2 -o "${TMP}/${ASSET}" "$ASSET_URL"
curl -fL --retry 3 --retry-delay 2 -o "${TMP}/checksums.txt" "$CHECKSUMS_URL" \
  || warn "checksums.txt unavailable; skipping verification (not recommended)"

if [ -f "${TMP}/checksums.txt" ] && [ -n "$SHA_TOOL" ]; then
  EXPECTED="$(grep -E "${ASSET}\$" "${TMP}/checksums.txt" | awk '{print $1}')"
  ACTUAL="$(${SHA_TOOL} "${TMP}/${ASSET}" | awk '{print $1}')"
  if [ -z "$EXPECTED" ]; then
    warn "no checksum entry for ${ASSET}; skipping verification"
  elif [ "$EXPECTED" != "$ACTUAL" ]; then
    die "checksum mismatch: expected ${EXPECTED}, got ${ACTUAL}"
  else
    ok "SHA-256 verified"
  fi
fi

step "Unpacking"
tar -xzf "${TMP}/${ASSET}" -C "$TMP"
[ -f "${TMP}/${APP}" ] || die "archive did not contain the ${APP} binary"
chmod +x "${TMP}/${APP}"

step "Installing to ${BIN_DIR}/${APP}"
if [ -n "${SUDO:-}" ]; then
  $SUDO install -m 0755 "${TMP}/${APP}" "${BIN_DIR}/${APP}"
else
  install -m 0755 "${TMP}/${APP}" "${BIN_DIR}/${APP}"
fi
if [ -f "${TMP}/.env.example" ]; then
  mkdir -p "$SHARE_DIR"
  install -m 0644 "${TMP}/.env.example" "${SHARE_DIR}/.env.example"
  ok "template saved: ${SHARE_DIR}/.env.example"
fi

# ----------------------------------------------------------------------------
# seed .env (never overwrite an existing one)
# ----------------------------------------------------------------------------
if [ -n "$ENV_FILE_ARG" ]; then
  ENV_PATH="$ENV_FILE_ARG"
else
  ENV_PATH="${CONFIG_DIR}/.env"
fi
mkdir -p "$(dirname "$ENV_PATH")"

pick_free_port() {
  local p="$1"
  if have ss; then
    while ss -tln 2>/dev/null | grep -q ":${p} "; do
      p=$((p + 1))
      [ "$p" -gt 3470 ] && die "no free port found in 3457-3470"
    done
  fi
  printf '%s' "$p"
}

if [ -f "$ENV_PATH" ]; then
  warn "existing config kept: ${ENV_PATH} (delete it to re-seed)"
else
  step "Seeding config ${ENV_PATH}"
  if [ -n "$PORT_ARG" ]; then
    PORT="$PORT_ARG"
  else
    PORT="$(pick_free_port 3457)"
    [ "$PORT" = "3457" ] || warn "port 3457 busy - using ${PORT}"
  fi

  ADMIN_TOKEN="$(head -c 24 /dev/urandom | base64 | tr -d '=+/' | cut -c1-20)"

  # token resolution order:
  #   1. --token flag (explicit)
  #   2. CLI login auto-discovery done by the proxy itself at runtime
  #      (~/.config/{freebuff,manicode,codebuff}/credentials.json)
  #   3. AUTH_TOKENS empty -> bridge mode (clients bring their own token)
  TOKEN_LINE=""
  if [ -n "$TOKEN_ARG" ]; then
    TOKEN_LINE="AUTH_TOKENS=${TOKEN_ARG}"
  else
    TOKEN_LINE="# AUTH_TOKENS=cb_xxx,cb_yyy  (empty = auto-discover CLI login, or bridge mode)"
  fi

  {
    echo "# unified freebuff proxy - generated by scripts/install.sh"
    echo "# upstream docs: https://github.com/trefeon/freebuff-proxy"
    echo "LISTEN_ADDR=127.0.0.1:${PORT}"
    echo "UPSTREAM_BASE_URL=https://www.codebuff.com"
    echo "COST_MODE=free"
    echo "SAFE_MODE=true"
    echo "DASHBOARD_ENABLED=true"
    echo "ADMIN_TOKEN=${ADMIN_TOKEN}"
    echo "${TOKEN_LINE}"
  } > "$ENV_PATH"
  chmod 600 "$ENV_PATH"
  ok "config seeded (chmod 600). Dashboard admin token is inside."
fi

# ----------------------------------------------------------------------------
# launch + verify
# ----------------------------------------------------------------------------
PORT_USED="$(grep -E '^LISTEN_ADDR=' "$ENV_PATH" | grep -oE '[0-9]+$')"
: "${PORT_USED:=3457}"

if [ "$NO_START" -eq 1 ]; then
  step "--no-start given; skipping launch"
else
  step "Starting proxy"
  LOG_DIR="${XDG_STATE_HOME:-${HOME}/.local/state}"
  mkdir -p "$LOG_DIR"
  LOG_FILE="${LOG_DIR}/${APP}.log"
  setsid bash -c "\"${BIN_DIR}/${APP}\" serve >> \"${LOG_FILE}\" 2>&1" < /dev/null &
  disown
  ok "launched (log: ${LOG_FILE}); waiting for /healthz"
  HEALTH_URL="http://127.0.0.1:${PORT_USED}/healthz"
  HEALTHY=0
  for _ in $(seq 1 30); do
    sleep 1
    if curl -fsS -m 2 "$HEALTH_URL" >/dev/null 2>&1; then
      HEALTHY=1
      ok "healthy: ${HEALTH_URL}"
      break
    fi
  done
  [ "$HEALTHY" -eq 1 ] || warn "not healthy after 30s - check ${LOG_FILE}"
fi

# ----------------------------------------------------------------------------
# summary
# ----------------------------------------------------------------------------
cat <<SUMMARY

${G}${B}Unified Freebuff Proxy installed.${X}

  Binary:     ${BIN_DIR}/${APP}
  Config:     ${ENV_PATH}
  Log:        ${XDG_STATE_HOME:-${HOME}/.local/state}/${APP}.log
  Endpoint:   http://127.0.0.1:${PORT_USED}/v1
  Dashboard:  http://127.0.0.1:${PORT_USED}/admin  (admin token in ${ENV_PATH})

  Default model:  ${B}z-ai/glm-5.3-flash${X}

  Manage tokens:  edit AUTH_TOKENS in ${ENV_PATH}, then reload:
    curl -X POST "http://127.0.0.1:${PORT_USED}/admin/reload" \\
         -H "Authorization: Bearer \$(grep ADMIN_TOKEN ${ENV_PATH} | cut -d= -f2)"

  Uninstall:  kill the process, then remove ${BIN_DIR}/${APP} and ${ENV_PATH}

SUMMARY
exit 0

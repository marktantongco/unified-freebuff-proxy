#!/usr/bin/env bash
# fetch-proxies.sh — Fetch fresh US SOCKS5 proxies from multiple sources
# Usage: ./fetch-proxies.sh [output_file]
set -euo pipefail

OUT="${1:-proxies.txt}"
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

echo "[$(date -Iseconds)] Fetching US SOCKS5 proxies..."

# Source 1: proxifly/free-proxy-list (US SOCKS5, validated every 5min)
curl -sL --max-time 10 \
  "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt" \
  | grep -E '^socks5://' \
  | sed 's/socks5:\/\///' \
  >> "$TMP" 2>/dev/null || true

# Source 2: ProxyScrape API (US SOCKS5, validated every minute)
curl -s --max-time 10 \
  "https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&proxy_format=protocolipport&format=text&protocol=socks5&country=us&timeout=5000" \
  | grep -E '^socks5://' \
  | sed 's/socks5:\/\///' \
  >> "$TMP" 2>/dev/null || true

# Source 3: Mohammedcha/ProxRipper (auto-updated every 15min)
curl -sL --max-time 10 \
  "https://raw.githubusercontent.com/mohammedcha/ProxRipper/main/full_proxies/socks5.txt" \
  | grep -vE '^#|^$' \
  | head -200 \
  >> "$TMP" 2>/dev/null || true

# Source 4: Firmfox/Proxify
curl -sL --max-time 10 \
  "https://raw.githubusercontent.com/Firmfox/proxify/main/proxy/socks5.txt" \
  | grep -vE '^#|^$' \
  | head -100 \
  >> "$TMP" 2>/dev/null || true

# Deduplicate and format as socks5://ip:port
sort -u "$TMP" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+$' | sed 's/^/socks5:\/\//' > "$OUT"

TOTAL=$(wc -l < "$OUT")
echo "[$(date -Iseconds)] Fetched $TOTAL unique US SOCKS5 proxies -> $OUT"

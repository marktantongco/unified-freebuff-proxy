#!/usr/bin/env bash
# test-proxies.sh — Test a list of socks5://ip:port proxies for connectivity
# Usage: ./test-proxies.sh <proxy_list_file> [max_tests]
set -euo pipefail

FILE="${1:?Usage: test-proxies.sh <proxy_list_file> [max_tests]}"
MAX="${2:-30}"
TIMEOUT=5

echo "Testing up to $MAX proxies from $FILE (timeout=${TIMEOUT}s each)..."
echo ""

WORKING=0
i=0
while IFS= read -r proxy && [ $i -lt $MAX ]; do
  i=$((i + 1))
  proxy=$(echo "$proxy" | tr -d '[:space:]')
  [[ -z "$proxy" || "$proxy" == \#* ]] && continue

  start_ms=$(($(date +%s%N) / 1000000))
  result=$(curl -s --max-time "$TIMEOUT" --proxy "$proxy" "http://httpbin.org/ip" 2>/dev/null)
  end_ms=$(($(date +%s%N) / 1000000))
  latency=$((end_ms - start_ms))

  if echo "$result" | grep -q "origin"; then
    ip=$(echo "$result" | python3 -c "import sys,json; print(json.load(sys.stdin)['origin'])" 2>/dev/null || echo "?")
    WORKING=$((WORKING + 1))
    printf "  [%2d] OK   %-30s  IP=%-15s  %4dms\n" "$i" "$proxy" "$ip" "$latency"
  else
    printf "  [%2d] FAIL %-30s  %4dms\n" "$i" "$proxy" "$latency" >&2
  fi
done < "$FILE"

echo ""
echo "Result: $WORKING / $i working"

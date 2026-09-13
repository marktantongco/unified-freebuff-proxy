#!/usr/bin/env bash
# smoke.sh — pre-deploy smoke test for freebuff-unified
#
# Probes the same endpoints health.sh does, but with stricter asserts:
#   - 200 required (no 401/403 accepted)
#   - response shape checked (must be JSON with required keys)
#   - latency threshold (warn if >2s)
#   - exits non-zero on any failure → blocks CI
#
# Usage:
#   smoke.sh                  # full suite
#   smoke.sh --quick          # only /healthz + /v1/models (skip deep probes)
#   smoke.sh --port 18080     # probe a different port (testing builds)

set -uo pipefail

QUICK=0
PORT="${PORT:-18080}"
for arg in "$@"; do
    case "$arg" in
        --quick) QUICK=1 ;;
        --port)  shift; PORT="$1" ;;
        -h|--help)
            sed -n '2,12p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

KEY="${FREEBUFF_API_KEY:-fbu_40c2492e432d5c92dfbc2166dfa57afb2b4349d0636fb7e1}"
OWL_KEY="${OWL_LOCAL_KEY:-sk-your-local-key}"
BASE="http://127.0.0.1:${PORT}"
PASS=0
FAIL=0
WARN=0

check() {
    local name="$1" url="$2" expect="$3" auth="${4:-}" check_shape="${5:-}"
    local start end dur_ms body code
    start=$(python3 -c "import time; print(int(time.time()*1000))")
    body=$(curl -s -m 10 -o /tmp/smoke.tmp -w "%{http_code}" \
        ${auth:+-H "Authorization: Bearer $auth"} "$url" 2>/dev/null || echo "000")
    code=$body
    end=$(python3 -c "import time; print(int(time.time()*1000))")
    dur_ms=$((end - start))

    local status="OK"
    if [[ "$code" != "$expect" ]]; then status="FAIL"; fi
    if [[ $dur_ms -gt 2000 ]]; then status="WARN"; fi
    if [[ -n "$check_shape" && -s /tmp/smoke.tmp ]]; then
        if ! python3 -c "$check_shape" < /tmp/smoke.tmp 2>/dev/null; then
            status="FAIL"
        fi
    fi

    case "$status" in
        OK)   printf '  \033[32m✓\033[0m %-40s %s (%dms)\n' "$name" "$code" "$dur_ms"; PASS=$((PASS+1)) ;;
        WARN) printf '  \033[33m!\033[0m %-40s %s (%dms — slow)\n' "$name" "$code" "$dur_ms"; WARN=$((WARN+1)); PASS=$((PASS+1)) ;;
        FAIL) printf '  \033[31m✗\033[0m %-40s %s (expected %s, %dms)\n' "$name" "$code" "$expect" "$dur_ms"; FAIL=$((FAIL+1)) ;;
    esac
}

printf '\n\033[1m== Smoke: %s ==\033[0m\n' "$BASE"

# Public endpoints (no auth)
check "GET /healthz (200)" \
    "$BASE/healthz" 200 \
    '' \
    'import json,sys; d=json.load(sys.stdin); assert "status" in d and d["status"]=="ok", f"missing status: {d}"'

check "GET /ai-stack/status (200)" \
    "$BASE/ai-stack/status" 200 \
    '' \
    'import json,sys; d=json.load(sys.stdin); assert "infrastructure" in d, f"missing infrastructure key"'

# Key-required endpoints
check "GET /v1/models (200)" \
    "$BASE/v1/models" 200 \
    "$KEY" \
    'import json,sys; d=json.load(sys.stdin); assert "data" in d and len(d["data"])>0, "no models"'

check "GET /v1/lmarena/evals (200)" \
    "$BASE/v1/lmarena/evals" 200 \
    "$KEY" \
    'import json,sys; d=json.load(sys.stdin); assert "evals" in d, "missing evals key"'

check "GET /v1/lmarena/leaderboard (200)" \
    "$BASE/v1/lmarena/leaderboard?top=1" 200 \
    "$KEY" \
    'import json,sys; d=json.load(sys.stdin); assert "entries" in d and len(d["entries"])>0, "no leaderboard entries"'

check "GET /lmarena/healthz (200)" \
    "$BASE/lmarena/healthz" 200 \
    "$KEY" \
    'import json,sys; d=json.load(sys.stdin); assert d.get("status") in ("ok","healthy"), f"not healthy: {d}"'

check "GET /hermes/healthz (200)" \
    "$BASE/hermes/healthz" 200 \
    "$KEY" \
    'import json,sys; d=json.load(sys.stdin); assert d.get("status") in ("ok","healthy"), f"not healthy: {d}"'

check "GET /stealth/status (200)" \
    "$BASE/stealth/status" 200 \
    "$KEY" \
    'import json,sys; d=json.load(sys.stdin); assert "status" in d, "missing status"'

# Auth enforcement
check "GET /v1/models no-auth (401)" \
    "$BASE/v1/models" 401 \
    '' \
    ''

if [[ $QUICK -eq 0 ]]; then
    # Owl-agent sidecar (no /health endpoint, probe /v1/models + /metrics)
    check "owl-agent /v1/models (200)" \
        "http://127.0.0.1:8080/v1/models" 200 \
        "$OWL_KEY" \
        'import json,sys; d=json.load(sys.stdin); assert "data" in d and len(d["data"])>0, "no models"'

    check "owl-agent /metrics (200)" \
        "http://127.0.0.1:9101/metrics" 200 \
        '' \
        'import sys; lines=sys.stdin.readlines(); assert len(lines)>1, "empty metrics"'

    # Eval harness write cycle
    EID=$(curl -s -m 10 -X POST -H "Authorization: Bearer $KEY" \
        -H "Content-Type: application/json" \
        -d '{"name":"smoke-test-'"$(date +%s)"'"}' \
        "$BASE/v1/lmarena/evals" | python3 -c "import json,sys;print(json.load(sys.stdin)['eval']['id'])" 2>/dev/null)
    if [[ -n "$EID" ]]; then
        printf '  \033[32m✓\033[0m %-40s %s\n' "POST /v1/lmarena/evals (created $EID)" "201"
        PASS=$((PASS+1))
        curl -s -m 5 -X DELETE -H "Authorization: Bearer $KEY" \
            "$BASE/v1/lmarena/evals/$EID" >/dev/null
    else
        printf '  \033[31m✗\033[0m %-40s eval creation failed\n' "POST /v1/lmarena/evals"
        FAIL=$((FAIL+1))
    fi
fi

printf '\n\033[1mSummary: %d pass, %d fail, %d slow\033[0m\n' "$PASS" "$FAIL" "$WARN"
[[ $FAIL -eq 0 ]] || exit 1

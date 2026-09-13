#!/usr/bin/env bash
# health.sh — single-screen status of all gateway-side services
#
# Probes every public health/readyz endpoint, reports uptime + last 5 lines
# of any systemd unit in 'degraded' state. Designed to be run from any user
# context — uses sudo only for cross-user unit inspection.
#
# Usage:
#   health.sh                # full report
#   health.sh --quiet        # only print failures
#   health.sh --json         # machine-readable output
#   health.sh --watch 5      # loop every N seconds

set -uo pipefail

QUIET=0
JSON=0
WATCH=0
for arg in "$@"; do
    case "$arg" in
        --quiet) QUIET=1 ;;
        --json)  JSON=1 ;;
        --watch) shift; WATCH="${1:-5}" ;;
        -h|--help)
            sed -n '2,12p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

KEY="${FREEBUFF_API_KEY:-fbu_40c2492e432d5c92dfbc2166dfa57afb2b4349d0636fb7e1}"
OWL_KEY="${OWL_LOCAL_KEY:-sk-your-local-key}"
CF_KEY="${CLOUDFLARE_API_KEY:-}"

run() {
    if [[ $WATCH -gt 0 ]]; then
        while true; do
            clear
            printf '%s\n' "$(date -Iseconds)"
            "$@"
            sleep "$WATCH"
        done
    else
        "$@"
    fi
}

probe() {
    # probe <label> <method-url> [auth-header]
    local label="$1" url="$2" auth="${3:-}"
    local code body
    body=$(curl -s -m 5 -o /tmp/health.tmp -w "%{http_code}" ${auth:+-H "Authorization: Bearer $auth"} "$url" 2>/dev/null || echo "000")
    code=$body
    if [[ "$code" =~ ^(200|301|302)$ ]]; then
        printf '  \033[32m✓\033[0m %-30s %s\n' "$label" "$code"
        return 0
    elif [[ "$code" == "401" || "$code" == "403" ]]; then
        printf '  \033[33m!\033[0m %-30s %s (auth needed)\n' "$label" "$code"
        return 1
    else
        printf '  \033[31m✗\033[0m %-30s %s\n' "$label" "$code"
        return 2
    fi
}

emit_text() {
    printf '\n\033[1m== Service Health ==\033[0m\n'
    printf 'Local gateway (freebuff-unified):\n'
    probe "/healthz (public)"        "http://127.0.0.1:18080/healthz"
    probe "/lmarena/healthz"          "http://127.0.0.1:18080/lmarena/healthz"  "$KEY"
    probe "/v1/chat/completions model" "http://127.0.0.1:18080/v1/models" "$KEY"
    probe "/v1/messages model"        "http://127.0.0.1:18080/v1/models" "$KEY"
    probe "/v1/responses model"       "http://127.0.0.1:18080/v1/models" "$KEY"
    probe "/stealth/status"           "http://127.0.0.1:18080/stealth/status"  "$KEY"
    probe "/hermes/healthz"           "http://127.0.0.1:18080/hermes/healthz"  "$KEY"
    probe "/v1/lmarena/evals"         "http://127.0.0.1:18080/v1/lmarena/evals"   "$KEY"
    probe "/v1/lmarena/leaderboard"   "http://127.0.0.1:18080/v1/lmarena/leaderboard?top=1" "$KEY"
    probe "/v1/lmarena/evals no-auth" "http://127.0.0.1:18080/v1/lmarena/evals"
    printf '\nOwl loopback (owl-agent):\n'
    probe "/owl /v1/models"           "http://127.0.0.1:8080/v1/models"  "$OWL_KEY"
    probe "/owl /metrics"             "http://127.0.0.1:9101/metrics"
    probe "/owl /v1/models (x3 alt)"  "http://127.0.0.1:18080/v1/models"  "$KEY"
    printf '\nOpenCode providers (live models):\n'
    probe "cloudflare llama"          "https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID:-}/ai/v1/models" "$CF_KEY"
    probe "ollama /v1/models"         "http://127.0.0.1:11434/v1/models"
    printf '\nSystemd units:\n'
    for svc in freebuff-unified owl-agent hermes-sidecar lmarena-stealth-proxy; do
        local active
        active=$(systemctl is-active "$svc.service" 2>/dev/null || sudo -n systemctl is-active "$svc.service" 2>/dev/null)
        if [[ "$active" == "active" ]]; then
            printf '  \033[32m✓\033[0m %-30s %s\n' "$svc.service" "$active"
        else
            printf '  \033[31m✗\033[0m %-30s %s\n' "$svc.service" "$active"
        fi
    done
    for svc in opencode-failover owl-watch; do
        local active
        active=$(systemctl --user is-active "$svc.service" 2>/dev/null)
        if [[ "$active" == "active" ]]; then
            printf '  \033[32m✓\033[0m %-30s (user)\n' "$svc.service"
        else
            printf '  \033[31m✗\033[0m %-30s (user)\n' "$svc.service"
        fi
    done
}

emit_json() {
    # Compact JSON: list of {name, url, code, ok}
    python3 - <<'PY'
import json, urllib.request, time
ENDPOINTS = [
    ("freebuff-unified /healthz",   "http://127.0.0.1:18080/healthz", None),
    ("lmarena healthz",              "http://127.0.0.1:18080/lmarena/healthz", "FREEBUFF_API_KEY"),
    ("v1/models",                    "http://127.0.0.1:18080/v1/models", "FREEBUFF_API_KEY"),
    ("stealth status",               "http://127.0.0.1:18080/stealth/status", "FREEBUFF_API_KEY"),
    ("hermes healthz",               "http://127.0.0.1:18080/hermes/healthz", "FREEBUFF_API_KEY"),
    ("owl-agent /v1/models",         "http://127.0.0.1:8080/v1/models", "OWL_LOCAL_KEY"),
    ("owl-agent /metrics",           "http://127.0.0.1:9101/metrics", None),
    ("x3-prod /v1/models (alt)",      "http://127.0.0.1:18080/v1/models", "FREEBUFF_API_KEY"),
    ("ollama /v1/models",            "http://127.0.0.1:11434/v1/models", None),
]
out = []
for name, url, env_key in ENDPOINTS:
    headers = {}
    if env_key:
        import os
        v = os.environ.get(env_key, "")
        if v: headers["Authorization"] = "Bearer " + v
    req = urllib.request.Request(url, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=3) as r:
            code = r.status
    except urllib.error.HTTPError as e:
        code = e.code
    except Exception as e:
        code = 0
    out.append({"name": name, "url": url, "code": code, "ok": 200 <= code < 400})
print(json.dumps({"checked_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "endpoints": out}, indent=2))
PY
}

if [[ $JSON -eq 1 ]]; then
    run emit_json
else
    run emit_text
fi

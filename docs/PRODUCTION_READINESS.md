# Production Readiness Roadmap

**Audit date**: 2026-09-14
**Stack scope**: freebuff-unified :18080, owl-agent-stack (x1 user), opencode CLI integration

## ✅ Done (this session)

| # | Area | Item |
|---|---|---|
| 1 | Reliability | Owl-agent.service supervisor wrapper (Pattern B) — bash loop kills stale PID + execs fresh |
| 2 | Reliability | Owl-server exit-78 patch (Pattern C) — distinguishes EADDRINUSE from real config errors |
| 3 | Reliability | systemd `RestartPreventExitStatus=0 78`, `StartLimitIntervalSec=60`, `StartLimitBurst=10`, `TimeoutStopSec=15` |
| 4 | Observability | Owl-watch daemon — 30s poll, drift detection, auto-sync + restart |
| 5 | Operations | `bin/health.sh` — single-screen status of all services + ports + systemd units |
| 6 | Operations | `bin/smoke.sh` — pre-deploy smoke with shape + latency assertions |
| 7 | Operations | `bin/restart-prod.sh` — safe restart with pre/post health |
| 8 | Operations | `bin/secret-audit.sh` — agent-env diff vs backups |
| 9 | Operations | `bin/opencode-audit.sh` — opencode.json drift detection |
| 10 | Operations | `bin/rotate-backups.sh` — prune old config backups |
| 11 | Operations | `bin/runbook.sh` + 6 incident runbooks (provider-pool-dead, owl-agent-restart-loop, freebuff-unified-restart-loop, opencode-401-all-models, all-sidecars-down, secret-rotation-check) |
| 12 | Documentation | 7 ADRs in `docs/adr/` covering all major decisions |
| 13 | Observability | Owl-agent prometheus metrics on :9101 surfaced |

## 🟡 Medium effort (next session)

| # | Area | Item | Effort |
|---|---|---|---|
| 14 | Observability | Centralized metrics aggregation — `/health/all` endpoint in freebuff-unified that probes all services + writes status JSON for cron scraping | 4h |
| 15 | Observability | GenAI semantic metrics (`gen_ai.client.token.usage`, `gen_ai.server.time_to_first_token`) emitted from gateway + owl-agent | 1d |
| 16 | Reliability | Health-check dependency depth (`/readyz` with deep checks: ollama loaded, hermes connected, evals dir writable) | 4h |
| 17 | Operations | CI pipeline in `.github/workflows/` — lint, vet, test-race, smoke (calls `bin/smoke.sh` against test gateway) | 1d |
| 18 | Security | Pre-commit secret scanner on entire workspace (already configured for opencode-history; extend to freebuff-unified + owl-agent-stack) | 2h |
| 19 | Documentation | C4 architecture diagrams (Context, Container) for both gateways | 1d |
| 20 | Release | SemVer tags + CHANGELOG.md per component | 4h |

## 🔴 Big lifts (deferred — requires planning)

| # | Area | Item | Why big |
|---|---|---|---|
| 21 | Security | Vault migration for 31 secrets | Needs KMS + audit trail + rotation policy |
| 22 | Security | mTLS gateway ↔ sidecars via SPIFFE | Cert-manager + workload identity |
| 23 | Security | Per-tenant virtual API keys + RBAC | Requires gateway redesign (tenant table, scoped tokens) |
| 24 | Reliability | Chaos testing harness | Quarterly exercises: kill sidecars mid-request, inject latency, expire creds |
| 25 | Reliability | k6 load test to 2× expected peak | Need to define expected peak + test harness |
| 26 | Observability | Distributed tracing across gateway + agent + sidecars (OpenTelemetry) | Significant new dependency |
| 27 | Observability | LLM-specific alerts: p95 TTFT regression, token-cost spike, finish-reason drift | New dashboards + alertmanager config |
| 28 | Release | Canary deploys (5% → 25% → 100%) | Requires load balancer + auto-rollback hooks |
| 29 | Release | Signed artifacts (cosign + SBOM + SLSA L3) | Requires CI integration + registry |
| 30 | Documentation | Per-endpoint OpenAPI spec auto-generated + published | Spec generation in CI |

## Quick reference

```bash
# Health & smoke
~/workspace/freebuff-unified/bin/health.sh
~/workspace/freebuff-unified/bin/smoke.sh

# Restart safely
~/workspace/freebuff-unified/bin/restart-prod.sh

# Sync owl-agent-stack edits
~/workspace/freebuff-unified/bin/owl-sync --restart

# Opencode provider failover
journalctl --user -u opencode-failover.service -f

# Incident response
~/workspace/freebuff-unified/bin/runbook.sh list
~/workspace/freebuff-unified/bin/runbook.sh <incident-name>

# Audit
~/workspace/freebuff-unified/bin/secret-audit.sh
~/workspace/freebuff-unified/bin/opencode-audit.sh
```

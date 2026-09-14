# Integration Analysis: 4 Critical Services + owl-dns-synergy + owl-unified

**Date**: 2026-09-14
**Scope**: How freebuff-unified, owl-agent, opencode-failover, owl-watch
stack with owl-dns-synergy and unified-owl modules.

## Service map (verified live)

| Service | User | Port(s) | Source | Role |
|---|---|---|---|---|
| freebuff-unified | root | :18080, :9091 dashboard | `/home/x3/freebuff-unified/` | L7 gateway, OpenAI/Anthropic API, eval harness |
| owl-agent (`owl_server.py`) | x1 | :60000 API, :9101 metrics | `/home/x1/.owl-agent/` ← synced from `~/workspace/unified-owl/` | Proxy defense, chameleon fingerprints, MCP fetch |
| opencode-failover | x3 (user unit) | — (rewrites config) | `~/.local/bin/opencode-failover` | Polls providers every 60s, swaps active model |
| owl-watch | x3 (user unit) | — (runs owl-sync) | `~/.local/bin/owl-watch` | Polls unified-owl every 30s, syncs drift + restarts |
| owl-dns-synergy proxy | ? | :60000 (deadlocked, never bound) | `/opt/owl-dns-synergy/` | DNS-tunnel proxy (stale) |
| owl-dns-synergy server | ? | :60001, :9090 metrics | `/opt/owl-dns-synergy/` | Second OpenAI surface (`owl-default` model) |
| forward_proxy.py | x1 | :60100 (mesh) | `/home/x1/.owl-gateway/` | CONNECT-tunnel + mesh UDP broadcast |
| gost | x1 | :18181 HTTP, :18182 SOCKS5 | system | L4 mux feeding mitmdump |
| mitmdump | x1 | :8081 | `/home/x1/.owl-agent/venv/` + `clean_headers.py` | Header rewrite |
| caddy | x1 | :8443 TLS | system | TLS termination → :8080 |
| headroom | x3 | :8787 cache proxy | `/usr/local/bin/headroom` | Cache layer (opencode legacy path) |
| ollama | ollama | :11434 | system | Local models (qwen2.5:3b live) |
| hermes-sidecar | root | :3101 | `deps/hermes-service/` | TLS 1.3 / HTTP2 stealth fetch |
| lmarena-stealth-proxy | root | :3103 | `deps/lmarena-stealth-proxy/` | Arena sessions + eval relay |

## Synergies (what works together)

1. **freebuff-unified → hermes-sidecar → SOCKS5 pool**: stealth egress chain.
   Hermes roundtripper + pool refresher + metrics all wired. Verified via
   `/stealth/status` returning live egress IPs.
2. **freebuff-unified → owl-agent (:8080→:60000?)**: NO — owl provider in
   opencode.json points at `:8080` which is freebuff-proxy (x1), not
   owl-agent (:60000). Naming confusion only; no traffic flows gateway→owl.
3. **opencode-failover → all providers**: polls x3, owl, cloudflare, ollama,
   Zen slots; rewrites `model`/`small_model` on first OK. Verified flipping
   x3/mimo ↔ Zen free on quota reset.
4. **owl-watch → owl-sync → owl-agent.service**: edit unified-owl →
   30s poll → copy to x1+root → `--restart` (kill, port-clear,
   reset-failed, start). Verified end-to-end with chameleon_ai.py edit.
5. **owl-agent → chameleon_ai**: middleware auto-injected, RL scores feed
   `/chameleon/stats` (chrome131 best, 4 successes observed).
6. **Prometheus scrape surface**: owl-agent :9101/metrics (55 proxies),
   owl-dns-synergy :9090/metrics, x1 prometheus :9092, freebuff :9091
   dashboard. Four metric endpoints, no unified scrape config.

## Redundancies / conflicts found

| # | Finding | Severity | Action |
|---|---|---|---|
| F1 | `owl_dns_synergy.cli proxy` (PID 8536) targets :60000 but never bound (deadlocked, S-state). owl_server holds the port. | Medium | Kill 8536 if confirmed orphan; mask unit if unused |
| F2 | `:60001` second OpenAI surface (`owl-default` model) duplicates `:18080/v1/models`. Only differentiation: `owl-default` alias. | Low | Keep (unique alias) or retire if no clients use it |
| F3 | `forward_proxy.py` (:60100 mesh) — initially misread as orphan; actually healthy x1 user-unit child. | None | Leave alone |
| F4 | Chameleon observes inbound only; `ResilientClient` never calls `engine.report()`. RL scores train on API traffic, not provider outcomes. | Medium | Wire `report(domain, success, blocked)` into `proxy_defense.py` request path |
| F5 | `freebuff2api.translator.race_upstreams` simulates (sleep + 10% fail), never real httpx. | Low | Back with real calls or drop import |
| F6 | `owl_resilient_mcp.py` never executed; headroom serves MCP instead. | Low | Either deploy it or remove from opencode.json |
| F7 | `owl_dns_synergy/router_v3.py` (1446 lines) zero import sites. | Low | Delete |
| F8 | `us_relay/`, `auth/oauth.py` always stubbed (`enabled: false`). | Low | Delete or implement |
| F9 | owl_server + freebuff-unified both serve `/v1/chat/completions`. freebuff is real; owl is stub fallback. | Medium | Demote owl to lab-only (`/chameleon/stats`, `/fetch`, `/v1/models`) |
| F10 | `skills-local/` (86 dirs, ~50MB) duplicates `~/.agents/skills/` v24.0.0. Not synced by owl-watch. | Low | Delete or symlink |

## Integration plan (ordered)

### P0 — stop the bleeding (done 2026-09-14)
- [x] forward_proxy verified healthy (:60100), not orphan
- [ ] Kill PID 8536 (deadlocked :60000 claimant) — needs sudo, deferred
- [ ] Mask `owl-dns-synergy.service` proxy cmd if :60001 unused — needs client audit

### P1 — one API surface (open)
- [ ] freebuff-unified `:18080` is front door (real upstream, cost-mode=free)
- [ ] Demote owl_server `:60000` to lab (chameleon + fetch + models only)
- [ ] Remove `translator.py` stub import until backed by real httpx

### P2 — collapse owl_dns_synergy (open)
- [ ] Delete `router_v3.py`, `skills-local/`, `scripts_stack/` dups, `auth/`, `us_relay/`
- [ ] Keep `router.py` (558 lines, prod) as single canonical router

### P3 — wire Chameleon → ResilientClient (open)
- [ ] Call `chameleon_engine.report(domain, success, blocked)` per request

### P4 — unify observability (partially done)
- [x] `/health/all` aggregator (7 probes, 10s cache, X-Health-Cache header)
- [x] `/readyz` deep checks (evals_dir writable + all sidecars)
- [ ] Single Prometheus scrape config across :9101/:9090/:9092/:9091
- [ ] Grafana dashboard for chameleon gauges + freebuff metrics

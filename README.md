# freebuff-unified — Unified Freebuff Gateway

A single Go service that exposes FreeBuff/Codebuff free models through
OpenAI-shaped and Anthropic-shaped HTTP APIs, with stealth egress (TLS
fingerprinting + SOCKS5 pool), a token pool, a dashboard, and ai-stack status
reporting.

## Architecture (read this first)

**The native completion path is the source of truth.** The gateway serves
`/v1/chat/completions`, `/v1/messages`, and `/v1/messages/count_tokens` from
its own session manager, token pool, and upstream client
(`internal/session`, `internal/freebuff`, `internal/httpapi`).

The `proxy:` config block has two modes:

- **`mode: report` (default, unset)** — health reporting only: the backend URL
  feeds `/healthz`, `/ai-stack/status`, and the startup log; all `/v1/*`
  traffic is served by the native path.
- **`mode: passthrough`** — explicit front-door opt-in: `/v1/*` is relayed
  byte-level (SSE-safe, hop-by-hop headers stripped) to `backend_url`, whose
  sessions, token pool, and `/admin` dashboard do the protocol work.
  `/healthz` and `/ai-stack/status` remain native. Native `/v1` routes are
  not registered in this mode.

Activation requires `proxy.mode: "passthrough"` **and** a backend URL
(`FREEBUFF_PROXY_BACKEND` env wins over `proxy.backend_url`).

```
Clients (OpenAI/Anthropic SDKs)
   │  Bearer / x-api-key (auth middleware)
   ▼
Fiber app :18080  ── /v1/chat/completions ─┐
                  ── /v1/messages ─────────┤ native path:
                  ── /v1/models, /healthz  │ session manager → freebuff upstream
                  ── /stealth/status       │ (hermes sidecar transport)
                  ── /ai-stack/status      │
                                           ▼
                          https://www.codebuff.com
```

## Components

| Piece | Where | Role |
|---|---|---|
| Gateway binary | `cmd/freebuff` | serve/login/logout/check |
| HTTP API | `internal/httpapi` | routes, auth, streaming, heartbeats |
| Session manager | `internal/session` | session lifecycle, bootstrap dedup, polling |
| Upstream client | `internal/freebuff` | codebuff session + chat protocol |
| Stealth transport | `internal/stealth` | hermes roundtripper, SOCKS5 pool, refresher, metrics |
| Hermes sidecar | `deps/hermes-service` (+ vendored `deps/hermes`) | Node TLS 1.3 / HTTP2 fingerprint proxy on :3101 |
| Credentials | `internal/credentials` | flock-locked JSON store (auths/credentials.json) |
| Dashboard | `internal/dashboard` | probe engine + SSE UI on :9091 |
| Parallel / search | `internal/parallel`, `internal/websearch` | Parallel Web APIs; keyless DDG fallback |
| Config | `internal/config` | YAML load (+fsnotify watcher, currently not wired at boot) |

## Quick start

```bash
# build (Go >= 1.26)
go build -o bin/freebuff-unified ./cmd/freebuff

# configure (copy config.example.yaml → config.yaml, fill in your keys)
./bin/freebuff-unified check

# run
./bin/freebuff-unified serve
```

Endpoints: `GET /healthz`, `GET /v1/models`, `POST /v1/chat/completions`,
`POST /v1/messages`, `POST /v1/messages/count_tokens`, `GET /ai-stack/status`,
`GET /stealth/status`, `GET /proxy/verify`, plus the hermes and parallel
endpoints listed in `internal/httpapi/server.go`.

## Deployment

- systemd units: `deploy/systemd/` (gateway + hermes sidecar)
- `deploy/docker/` is intentionally empty (no supported container flow yet)

## History & context

- `SESSION_SUMMARY.md` — the 2026-09-03 merge session log (three-lineage merge)
- `planning/2026-09-09-freebuff-slow-investigation.md` — 5-minute-abort root-cause analysis
- `planning/2026-09-09-unification-audit.md` — full landscape audit (duplication, drift, security)

Upstream projects: trefeon/freebuff-proxy (MIT), Quorinex/FreeBuff2API (MIT),
kori-lab/hermes. This repo is a local unification of their ideas, not a fork.

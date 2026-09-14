# ADR 0007 — Opencode CLI routes through Owl engine proxy

**Status**: Accepted (2026-09-14)

## Context

Opencode CLI was originally aligned to a headroom cache proxy (`localhost:8787`). User asked to route through the Owl engine proxy instead for production-ready semantics.

## Decision

5 named providers in `~/.config/opencode/opencode.json`:

| Slot | Target | Source |
|---|---|---|
| `owl` | `http://127.0.0.1:8080/v1` | Owl loopback freebuff-unified (x1 stack) |
| `x3` | `http://127.0.0.1:18080/v1` | X3 prod freebuff-unified (this stack) |
| `opencode3` | Cloudflare Workers AI | `CLOUDFLARE_API_KEY` |
| `opencode4` | Ollama :11434 | literal `ollama` |
| `opencode5` | X3 alias (free, prod) | `FREEBUFF_API_KEY` |

All keys in `~/.config/opencode/agent-env` (chmod 600), referenced via `{env:VAR}` — no plaintext keys in config. Default model: `x3/mimo/mimo-v2.5` (only currently-working free tier).

## Consequences

- Zero plaintext keys in config file (vs prior `apiKey: "sk-caGvPsin..."`)
- Loopback providers don't require outbound network (work even if lmarena.ai is down)
- Cloudflare + Ollama fall back if loopback is unavailable
- Active model auto-rotates via `opencode-failover.service` based on per-provider probe results

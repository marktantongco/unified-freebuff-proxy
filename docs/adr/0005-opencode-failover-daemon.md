# ADR 0005 — Opencode CLI provider failover via polling daemon

**Status**: Accepted (2026-09-14)

## Context

Opencode CLI providers rotate availability: Zen free quota resets at 07:00 UTC daily, Cloudflare/ollama/loopback endpoints drift. Manual `opencode.json` edits per failure are error-prone.

## Decision

Two-layer:

1. **5 named providers** in `opencode.json`: `owl` (loopback :8080), `x3` (loopback :18080), `opencode3` (Cloudflare Workers AI), `opencode4` (Ollama :11434), `opencode5` (alias for x3)
2. **Background probe daemon** (`~/.local/bin/opencode-failover` + systemd user unit `opencode-failover.service`) polls each provider's `/v1/chat/completions` every 60s with a 1-token call. On OK, rewrites `model` + `small_model` to point at the first responding provider. Per-provider model fallbacks (`mimo`, `deepseek-v4-flash`, `glm-5.3-flash`, `deepseek-v3`) so 429-quota-exhausted providers still find a working model.

## Consequences

- 5-minute failure window: when Zen resets at 07:00 UTC, providers flip back automatically within 60s
- Status file: `~/.local/share/opencode/failover-status.json` (JSON, last probe + result per provider)
- No CLI restart needed — next opencode session picks up the new model. Active sessions keep their current model until restart.
- Per-provider model probes prevent 404-on-probe (e.g. cloudflare doesn't expose Zen-style model names)

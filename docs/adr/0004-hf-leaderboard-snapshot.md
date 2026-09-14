# ADR 0004 — Public leaderboard snapshot via HF datasets-server

**Status**: Accepted (2026-09-14)

## Context

The eval harness needed a public, ToS-clean source of model ratings. Two options evaluated:

- Parse lmarena.ai web pages (banned by ToS)
- Read HuggingFace `lmarena-ai/leaderboard-dataset` (CC-BY-4.0, official dump)

## Decision

Use the HF datasets-server JSON API: `https://datasets-server.huggingface.co/rows?dataset=lmarena-ai/leaderboard-dataset&config=text&split=latest`. The gateway:

1. Pages the full ~10k-row config with 8-worker atomic-offset dispenser
2. Filters to requested category (default `overall`)
3. Caches to disk under `lmarena.eval_dir` (24h TTL by default)
4. Serves via `GET /v1/lmarena/leaderboard?category=&top=`
5. Single-flight refresh via `sync/atomic` + `sync.Cond` — concurrent requests share one fetch

## Consequences

- Fully ToS-compliant (HF-hosted public dump, no lmarena.ai traffic)
- Cached + stale-while-revalidate — survives HF downtime
- No external key needed
- `top` query param caps results at 200 max
- ai-stack status surfaces top-5 in the cached snapshot (read-only, never triggers fetch)

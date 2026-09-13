# ADR 0003 — Manual blind A/B eval harness (no automated lmarena.ai fetch)

**Status**: Accepted (2026-09-14)

## Context

User asked for real model fetch from lmarena.ai. ToS explicitly prohibits automated access (Cloudflare Turnstile, account-pool gating, ban threats). Reverse-engineered tools (`lmarena2api`, `LMArena-Bridge`, `lmarena` Puppeteer SSE) are all ToS circumvention.

## Decision

Build a **manual eval harness** instead: human pastes model outputs from lmarena.ai (browsed by hand in any normal browser), gateway stores/blinds/scores only. No automated upstream fetch ever happens from the gateway.

Three layers:

1. **Sealed rounds** — `model_a` + `model_b` stored but hidden until `reveal` (no blind leakage)
2. **Bradley-Terry rating** — arena-rank methodology (weighted MLE, Elo-400 scale), weak tie-prior for sparse data
3. **Bulk import** — JSONL/CSV paste (≤500 records, atomic batch) so humans can dump a session in one POST

## Consequences

- ToS-compliant: zero automated lmarena.ai traffic from gateway
- Honest: humans drive the browser, gateway only stores
- Bradley-Terry matches official methodology (same math as `lmarena/arena-rank` Go package)
- Future: if lmarena.ai offers an official export API or public HF dataset for transcripts, can ingest via same import endpoint

## References

- github.com/lmarena/arena-rank (Apache-2.0)
- HF `lmarena-ai/leaderboard-dataset` (CC-BY-4.0) — public leaderboard data we DO ingest
- HF `lmarena-ai/arena-human-preference-140k` (gated) — possible future import source

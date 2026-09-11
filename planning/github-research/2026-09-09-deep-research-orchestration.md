# GitHub Research: Deep-Research Orchestration for Freebuff 🎯

**Research date:** 2026-09-09
**Researcher:** opencode (document-findings workflow)
**Goal:** Decide how to add a deep-research capability to the freebuff unified gateway (fast, keyed via Parallel + keyless DDG fallback), and enumerate the "special sauce" speed levers that make research feel instant.

> ⚠️ Note on freshness: Parallel's product surface is changing fast (GA `/v1/search` is recent, `-fast` processor guidance flipped in 2026-06). Findings reflect docs as of the research date. Re-verify before acting on anything older than ~3 months, especially processor tiers and endpoint versions (`/v1` vs `/v1beta` vs `/v1alpha`).

## Queries run

Web (parallel-search MCP):
```text
Parallel AI deep research API docs
parallel-web-search npm package
parallel-deep-research package
ai.parallel.ai deep research model
open source deep research agent framework 2026
LangChain deep research parallel search
gpt-researcher vs smolagents deep research
openai agents sdk deep research
```
Fetched full docs: `docs.parallel.ai/task-api/task-deep-research`, `docs.parallel.ai/task-api/guides/choose-a-processor`, `docs.parallel.ai/api-reference/search/search`.

npm (octocode): `parallel-web` (exists, v1.3.3), `parallel-web-search` (❌ empty — not a real package), `parallel-deep-research` (❌ empty), keywords `deep-research`, `parallel deep research`.

GitHub (octocode): `parallel-web/langchain-parallel` README.

Local grounding: `freebuff-unified/internal/parallel/client.go`, `internal/httpapi/handlers.go`, `cmd/freebuff/main.go`, `internal/config/config.go`.

## Landscape summary

### 1. Parallel's product — the four relevant surfaces

| Surface | Endpoint | Latency | Use for | Verdict |
|---|---|---|---|---|
| **Search** | `POST /v1/search` (GA) | `turbo` ~200ms / `fast` ~700ms / `basic` ~1s / `advanced` ~3s | scoping, fact-check, sub-queries | Already wired in freebuff |
| **Extract** | `POST /v1/extract` | seconds | turn top-N URLs into markdown/excerpts | Already wired in freebuff |
| **Task (Deep Research)** | `POST /v1/tasks/runs` | lite 10s–2min … ultra 3–25min, ultra8x 2h | full cited report, async | **Missing in freebuff** |
| **Responses** | `/v1/responses`, model=`parallel` | TTFT ~3s (speed tier) | latency-sensitive agentic research, streaming SSE | **Missing in freebuff** |

Key docs facts:
- **Search** now requires `search_queries` (1–5, 3–6 words). `objective` optional but sharpens recall. `fetch_policy` controls cache-vs-live (`max_age_seconds`, `timeout_seconds`, `disable_cache_fallback`). Default mode is `advanced` if omitted — explicit `fast`/`turbo` is the speed lever.
- **Task / Deep Research** is async: create → returns `run_id` (+ `interaction_id`); poll `GET /v1/tasks/runs/{id}/result` or stream `GET /v1/tasks/runs/{id}/events` (SSE, enabled via `enable_events:true`) or webhook. Two output schemas: `auto` (JSON) and `text` (markdown report with inline citations). Input cap **15k chars**. Deep Research tuned for `pro`/`ultra` families. **`-fast` variants (e.g. `pro-fast`, `ultra-fast`) are 2–5x faster at similar accuracy** — the official recommendation for interactive/agent-loop work.
- **Interaction chaining** for multi-turn: first call returns `interaction_id`; follow-ups pass `previous_interaction_id` and use a lighter `core` processor — follow-up questions don't re-run the whole research.
- **Processors** (execution time, queue time excluded): `lite` 45s p50, `base` 50s p50, `core` 1.5min p50, `pro` 3.5min p50, `ultra` 4min p50 · p90 up to 11min.
- **Responses API** (`model="parallel"`, OpenAI-compatible `/v1/responses` with `reasoning.effort` low/medium/high, streaming SSE, structured `text.format`) is Parallel's own recommendation over Task API when a caller is *waiting* — i.e. subagent calls.

### 2. `langchain-parallel` — the reference orchestration API (MIT, active)

Four Task surfaces, all defaulting to a **`-fast`** tier:
- `ParallelTaskRunTool` — LLM-callable tool, default `lite-fast`
- `ParallelDeepResearch` — Runnable for the single-input deep-research pattern, **default `pro-fast`**
- `ParallelTaskGroup` — bulk batch primitive, default `lite-fast`
- `ParallelEnrichment` — structured enrichment, default `core-fast`
Plus `ParallelWebSearchTool`, `ParallelExtractTool`, `ParallelSearchRetriever`, `ChatParallelWeb` (models `speed` ~3s / `lite` / `base` / `core`, citations in `response_metadata["basis"]`), and `parse_basis()` → `{citations_by_field, low_confidence_fields, interaction_id}`. Latest CHANGELOG confirms `ParallelDeepResearch` (default pro-fast) is the canonical deep-research primitive.

### 3. Open-source deep-research harnesses (for the native no-key path)

| Project | Shape | Fit for freebuff |
|---|---|---|
| **LangChain Open Deep Research / Deep Agents** (LangGraph) | Scope → Research (supervisor fans out to parallel sub-agents, isolated contexts) → Write. BYO model/search/MCP. MIT. | Best model for a native Go harness mirrored in spirit: plan → parallel fan-out → synthesize |
| **gpt-researcher `deep_agents/`** | Chief Editor (deep agent) + parallel Researcher sub-agents (gpt-researcher engine). 35.2 verified citations vs 18.6 baseline (+89%). Apache-2.0. | Confirms value of full-page reading vs raw snippets |
| **OpenAI Agents SDK cookbook** | Triage → Clarifier → Instruction Builder → Research agent (WebSearchTool + MCP), streams intermediate events. | Same shape; confirms event streaming for transparency |
| **JigsawStack `deep-research`** (npm `deep-research` 0.1.4, Apache-2.0) | TS/JS library: web search + reasoning + bibliography. | Lightweight TS reference; not Go |
| **octagon / gemini deep-research MCP servers** | MCP wrappers. | Only relevant if we expose deep-research as an MCP tool |

`parallel-web` (npm 1.3.3, MIT) is the official TS SDK — the only first-party package. `parallel-web-search` and `parallel-deep-research` are **not** published; "parallel-deep-research" in opencode config here refers to the MCP server name, not an upstream package.

## Recommendation

**Two-layer plan for freebuff:**

**Layer A — keyed fast path (when `PARALLEL_API_KEY` present):** passthrough/delegate to Parallel's own research brain instead of rebuilding it.
1. Add a thin **Task API client** to `internal/parallel/client.go` (create run, poll/result, events SSE + webhook HMAC-SHA256). This is a small diff — the HTTP plumbing pattern already exists in the file.
2. Expose `POST /v1/deep-research` (create → `{run_id, interaction_id}`, `accept: text/event-stream` for progress, async result at `GET /v1/deep-research/{id}`), mirroring the Task API so existing Parallel SDKs can talk to freebuff transparently.
3. Default processor **`pro-fast`**; honor `pro`/`ultra`:true for batch/offline. Bridge `enable_events` SSE through freebuff's existing Fiber streaming + 15s heartbeat machinery (already proven in the 5-min-abort fix).
4. Expose the **Responses API** (`/v1/responses` passthrough, model=`parallel`) for interactive/subagent research — freebuff then becomes the front door for Parallel Chat (`speed`), Search, Extract, Task, and Responses.

**Layer B — native keyless harness (zero cost, reuses DDG stealth):** a minimal scope→research→write loop in Go, mirroring Deep Agents:
1. `POST /v1/deep-research` when no key → LLM plans 3–5 sub-queries (freebuff upstream deepseek, `fast` temperature low, JSON out).
2. Fan out the sub-queries to `internal/websearch` (DDG stealth) **and** `internal/parallel` Search (`fast` mode) with bounded concurrency (5–8 via errgroup), then `internal/parallel` Extract (or stealth fetch) on the top 2–3 URLs per query.
3. Second LLM pass synthesizes a cited markdown report; stream frames as they land.

This is deliberately Parallel's own architecture (Search + Extract under a research harness) but with our keyless DDG fallback where the API key is absent. Do **not** rebuild a heavy LangGraph-style runtime — a fixed 3-phase graph is enough and lives happily in Go.

## The "special sauce" (speed levers, ranked)

1. **`-fast` processor family** — `pro-fast` is the single biggest win (2–5x vs `pro`, "similar accuracy"). Default to it everywhere interactive.
2. **Search `mode: fast`/`turbo` for scoping, `advanced` only for the final deep pass** — 700ms vs 3s per search; fan-out at 5–8 concurrent makes wall time ≈ one slow search.
3. **`fetch_policy` cache-first** — `max_age_seconds: 86400, disable_cache_fallback: false` means the index answers instead of blocking on live fetch, and stale-but-real bytes still flow when a fetch dies mid-research.
4. **Parallel Responses API (`model=parallel`) for anything a subagent waits on** — official answer to Task API queue time (which is *not* counted in the latency tables and can blow past them).
5. **Interaction chaining** — `previous_interaction_id` + `core` follow-ups skip re-research entirely; and `session_id` across Search/Extract groups one task's calls.
6. **Events SSE + freebuff heartbeat** — interactive progress + no 5-min infra abort (already proven on this gateway).
7. **`client_model` tuning** — set the consuming LLM id so Parallel tailors excerpt shaping (cheap, free speed/quality).
8. **Keyless DDG fallback** — already ships in freebuff (`internal/websearch`), so Layer B costs $0 to stand up and doubles as a health-check independent of key quota.
9. **Bounds on fan-out** — cap concurrent sub-queries (errgroup, limit 8) and cap per-run URL extracts (6–8) so a research task has a hard latency ceiling; the LLM does the triage, not the crawler.

## Open questions / follow-ups

- **API key state:** does x3 hold a paid `PARALLEL_API_KEY`? `/v1/deep-research` quality at `pro-fast` vs free codebuff upstream synthesis needs one real A/B run to decide Layer A vs B default.
- **Model availability:** confirm `parallel` model id is reachable from `https://api.parallel.ai` on this key (gateway currently only lists local upstream models).
- **Rate-limits:** freebuff global_rpm=120 / account_rpm=30 / client_rpm=60 — `fast`-mode fan-out of 8 with a 40-query research task stays well under account RPM, but Task API bursts should be shape-charged through the existing 3-tier limiter.
- **SSE-auth:** decide whether `/v1/deep-research/{id}/events` requires Bearer at connect time (fiber SSE upgrade path).
- **Webhook:** only add HMAC-SHA256 verify (`parallel-signature`) if async webhook delivery is a real requirement.
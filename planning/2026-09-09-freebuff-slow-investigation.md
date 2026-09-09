# Freebuff Slow Load + 5-min Timeout Investigation — 2026-09-09

## Question
Why does `freebuff` CLI load slow responding to query and when working in background, and how to prevent `connection timed out: no data was received from the server for 5 minutes, so the request was aborted`?

## Evidence Collected (actual timings on x3)

### 1. CLI cold start (`freebuff --help`)
- via `freebuff` launcher (node wrapper): `real 2.08s` (user 2.36s sys 0.35s)
- via direct binary `~/.config/manicode/freebuff --help`: `real 1.55s` (user 1.72s sys 0.26s, RSS 228M, 69k minor faults)
- launcher overhead = ~0.5s; binary itself = 1.55s for --help
- `launcher.js:ensureBinaryReady()` fast-path today is cheap: reads `freebuff-metadata.json` (2.6ms) + `/proc/cpuinfo` avx2 check (1.3ms), then returns because `0.0.172 >= 0.0.150`. No `getLatestVersion()` network call on healthy launches. `strace -c` top: `madvise`, `sched_yield`, `pread64`, `mmap` dominate — Bun 1.2 runtime init + 134M binary + 201k `tree-sitter.wasm` load.
- Conclusion: 1.5s is Bun compile baseline, not Node wrapper. `--help` still pays full Bun init (JSC + WASM) even though it needs almost nothing.

### 2. Gateway `freebuff-unified` startup (`cmd/freebuff/main.go:39`)
- `config check` (yaml only): 0.02s
- Full `runServe` boot timeline (observed): 
  - `godotenv.Load()` + flag parse + `config.LoadConfig` ~5ms
  - `freebuff.NewClient` cheap (clone transport, no I/O)
  - `hermesClient.Health(ctx)` **BLOCKING** at boot: `hermes.NewWithTimeout(120s)` then `Health()` with `http.Client{Timeout:60s}` via `context.Background()`. Measured healthy case 0.02s; if sidecar down → blocks 60s before falling through to `stealth mode ON` log. This is the #1 boot stall when hermes isn't running.
  - `stealth.HermesRoundTripper` wiring: clones `http.DefaultTransport`, sets 60s `ResponseHeaderTimeout`, wraps with `FailOpen:true`.
  - `Refresher.Start` schedules first refresh after 20s (non-blocking), then every 30m — but each cycle probes 150 candidates via sidecar concurrency 6, each probe `ProbeTimeout 12s` → worst 150*12/6 = 300s wall time contending on single-threaded hermes Node process (see stealth_status below).
  - `httpapi.NewApp` (Fiber) — no `ReadTimeout/WriteTimeout/IdleTimeout` set (Fiber defaults = 0 = no timeout). Health handlers call `probeBackend` synchronously per request (2× `http.Client{Timeout:2s}` + fallback), and `aiStackStatus` calls `client.Health` again per request (60s timeout if sidecar down).

### 3. Query path (single `POST /v1/chat/completions` non-stream)
- Handler `httpapi/handlers.go:ChatCompletions` → `chatService.Complete` → `session.Manager.EnsureActive` → `freebuff.Client.Complete`
- `EnsureActive` (`session/manager.go:EnsureActive`): loads credentials from disk `FileStore.Load` (read + json unmarshal) **every request** (no memory cache), then loops `Client.GetSession` (via hermes sidecar, avg 8.7s per sidecar request per `stealth_status` metrics) + potential `pollUntilActive` with `defaultPollInterval 2s`, `MaxPolls 0` = unbounded, until `SessionActive`. If queued, polls every 2s indefinitely until ctx cancelled (gateway passes `c` context = request context). No overall deadline except client's 5m.
- `freebuff.Client.Complete` (`freebuff/client.go:Complete`): `startAgentRun` (POST hermes), `buildUpstreamChatRequest`, `doChatRequest` (POST hermes), then `io.ReadAll(64k)` buffering. For long completions, hermes buffers **entire** response before replying to Go (sidecar `doStealth` waits for `await hermes(opts)` fully), Go then unmarshals. No incremental flush → client sees 0 bytes for minutes.
- Measured dry-run with bad auth: `5.272s` before error returned (includes session get + chat attempt via hermes, both hitting `auth_failed` then retry path). Successful long completions easily exceed 60–110s.
- `stealth_status` live: `sidecar_requests 10, avg 8790ms, max 70457ms, last 2469ms, fallbacks 0, sidecar_errors 0`. 8.7s avg sidecar latency is the dominant query cost; max 70s indicates SOCKS5 proxy hops.
- `refresher Status` live: `candidates 150, alive 29, probe_errors other 111 refused 10, probe_error_samples "context deadline exceeded"` — 74% failure, each deadline = 12s via sidecar. Next cycle will again tie up hermes for ~2–5m in background, starving foreground queries (single-threaded Node sidecar).

### 4. The 5-minute abort
Source: gateway or upstream infra kills idle connection after 5m with no bytes.

**Trigger chain for non-stream:**
1. Client sends `POST /v1/chat/completions` (non-stream, default for many SDKs).
2. `HermesRoundTripper` routes it through sidecar with `TimeoutMS 110000` (110s) and sidecar client `Timeout 120s`. If LLM needs >110s, sidecar returns 502; Go retries via `FailOpen` to fallback direct (60s header timeout) — but still buffered.
3. If LLM streams tokens slowly, Go's `Complete` waits for full JSON. Intermediate proxy (CF 524 is 100s, Vercel/infra is 300s) aborts after 5m of 0 bytes: `no data was received from the server for 5 minutes`.
4. Fiber has no `WriteTimeout`, so it never sends `:` heartbeat for non-stream (heartbeat only in `streamChatCompletions` every 15s via `writeStreamComment`). Non-stream has zero keep-alive.

**Why background makes it worse:** refresher's 150 probes contend on same hermes process; each probe holds the event loop for SOCKS5 handshake + TLS + `api.ipify.org` + `ipapi.co` geo lookup (5s). During a cycle, p95 sidecar latency spikes from 2s → 10–70s, pushing query over 5m threshold.

## Root Causes (ranked)

1. **Buffered non-stream via hermes** — no incremental bytes, 110s sidecar cap < 5m infra abort, no keep-alive.
2. **Hermes as single-threaded bottleneck** — foreground queries + background refresher share one Node process; `Concurrency 6` still serializes on event loop + SOCKS5 sockets.
3. **Blocking/sync probes on hot path** — `Health()` at boot, `probeBackend()` and `aiStackStatus→Health()` per `/healthz` request, `EnsureActive` disk + network per chat request with no cache.
4. **Bun cold start 1.5s** — 134M binary + JSC + tree-sitter.wasm paid even for `--help`; launcher adds 0.5s but not the main cost.
5. **No deadline propagation** — gateway passes `c` (fiber ctx) but upstream hermes calls use background-like timeouts; client 5m abort not surfaced as retryable.

## Fixes (orchestrated plan)

### Immediate (prevent 5m abort) — 1 day
- [ ] **Default to streaming.** Change CLI/gateway default `stream:true` for chat completions; document `stream:false` as buffered and slower. Already `streamChatCompletions` bypasses hermes and sends heartbeat every 15s — clients won't hit 5m.
- [ ] **Raise hermes caps for non-stream** to >5m: `stealthUpstreamTimeoutMS 280_000`, `stealthUpstreamClientTimeout 300s`, sidecar `DEFAULT_TIMEOUT 280_000` for codebuff upstream only (keep ipify probes at 12s). Add per-route timeout: codebuff.com → 280s, others → 15s.
- [ ] **Add keep-alive for non-stream.** In `handlers.ChatCompletions` non-stream branch, flush headers early (`c.Set("Content-Type","application/json")` + `c.Status(200)` + `Flush`) and/or send `:` comment every 15s while waiting on `Upstream.Complete` via goroutine, so infra sees bytes.
- [ ] **Fiber timeouts:** set `ReadTimeout 30s, WriteTimeout 310s, IdleTimeout 120s` in `NewApp` so 5m abort becomes explicit 502 with JSON error, not silent disconnect.
- [ ] **Make boot Health non-blocking.** Wrap `hermesClient.Health` in `context.WithTimeout(2s)` + goroutine; log async, don't block `runServe`. Same for `extraHealth` probes: cache last result 5s, serve stale.

### Short-term (reduce p95) — 1 week
- [ ] **Credential + session cache.** `FileStore` add `sync.RWMutex` + `fsnotify` or 1s TTL cache; `Manager` cache last `SessionActive` 30s keyed by model. Eliminates disk read + extra `GetSession` per request.
- [ ] **Isolate refresher from foreground.** Run refresher probes via direct SOCKS5 dial (no hermes) or second hermes instance on :3102; or lower `Concurrency 2`, `ProbeTimeout 8s`, and add jitter + circuit breaker: skip cycle if `sidecar_requests` latency >5s.
- [ ] **Refresher geo off hot path.** Batch `isUS` via cached ipapi response or disable `geofilter` during high load; currently `isUS` does `GET https://ipapi.co/<ip>/json` 5s per alive IP serially.
- [ ] **CLI fast path for --help/--version.** Patch `launcher.js:main()` to handle `--help`/`-h`/`--version`/`-v` **before** `ensureBinaryReady()` (just `fs.existsSync` check), saving 1.5s Bun spawn when binary missing.

### Medium-term (cold start) — 2 weeks
- [ ] **Bun compile optimization:** `bun build --compile --minify --sourcemap --bytecode` + `--target=bun-linux-x64-baseline` already exists, but try `--compile --smol` or split `tree-sitter.wasm` lazy load (load only for code-edit tools, not `--help`). Measure with `hyperfine`.
- [ ] **Persistent daemon mode for CLI.** Keep `freebuff` binary warm as `freebuff daemon` (like `codebuff --daemon`), CLI wrapper talks over unix socket `/tmp/freebuff.sock` — 2s → 20ms for subsequent queries. Useful for background agents.
- [ ] **Pre-warm session.** On gateway start, `Manager.EnsureActive(defaultModel)` async after 5s; keeps `session: active` so first user query skips queue poll.

## How to verify
- `hyperfine 'freebuff --help' ' ~/.config/manicode/freebuff --help'` before/after launcher fast-path.
- `curl -w '%{time_total}' http://127.0.0.1:18080/healthz` and `ai-stack/status` before/after Health caching (should drop from ~0.15s to <0.02s under sidecar down).
- `curl -N -X POST .../v1/chat/completions -d '{"stream":true}'` should emit `:` every 15s (check `tcpdump`).
- `go test ./internal/... -run TestRefresher` with `Concurrency 2` should show <30s cycle vs 300s.
- Longest repro: `timeout 400 curl --no-buffer ... stream:true` with deepseek 4k token prompt should not hit 5m abort.


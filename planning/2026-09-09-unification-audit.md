# Freebuff / Unified Proxy Landscape — Deep-Dive Audit — 2026-09-09

Scope: all freebuff/unified related directories on this host, their runtime topology,
code relationships, duplication, and unification gaps.

---

## 1. Landscape map

### Repos & directories

| Path | Provenance | Role | Status |
|---|---|---|---|
| `freebuff-unified/` | Local, **no commits yet** (all files untracked, no .gitignore) | The unified Go gateway (main subject) | Active, running as `freebuff-unified.service` |
| `unified-freebuff-proxy/` | `github.com/marktantongco/unified-freebuff-proxy` | Installer/docs repo wrapping upstream `trefeon/freebuff-proxy` (prebuilt binaries) | Active, uncommitted edits to README.md + install.sh (+86/-21), shellcheck CI |
| `freebuff-unified/deps-fb2api/` | Clone of `github.com/Quorinex/FreeBuff2API` @ a1c1035 | Go port of FreeBuff2API | **Dead weight — not imported by unified source** |
| `freebuff-unified/deps/freebuff2api/` | Clone of same repo, same commit | Same as above | **Byte-identical duplicate of deps-fb2api** |
| `freebuff-unified/deps/hermes/` | Vendored `@kori_xyz/hermes` 1.3.4 (388K) | Node stealth client (TLS 1.3 / HTTP2) | Used via hermes-service sidecar |
| `freebuff-unified/deps/hermes-service/` | Local sidecar wrapper (336K incl. node_modules) | HTTP wrapper over hermes on :3101 | Used, `hermes-sidecar.service` |
| `/opt/freebuff/go/freebuff-proxy/` | Upstream trefeon freebuff-proxy, user `freebuff` | Original stealth proxy | Running `freebuff-proxy.service` on **:3457** |
| `/opt/freebuff/python/Freebuff2API-Optimized/` | Python/FastAPI gateway + admin panel | Freebuff2API Python impl | Running `freebuff2api.service` (:31000) + `freebuff2api-admin.service` (:18432?) |
| `~/.local/bin/freebuff-unified` | Symlink → `~/freebuff-unified/bin/freebuff-unified` | CLI entry | OK |
| `/home/x1/.owl-agent/engine/bin/freebuff-unified` | Second host account (x1) | Independent deployment | Running under user x1 |

### Binaries in `freebuff-unified/bin/`

- `freebuff` (23MB, Go ELF) — misleading name: despite the name it only accepts
  `-config` (an older freebuff-unified build), **not** the upstream `freebuff-proxy`
  binary with `-doctor/-test-token/serve` subcommands.
- `freebuff-unified` == `freebuff-unified.new` — byte-identical (same BuildID); the
  `.new` file is a leftover atomic-rename artifact.
- Owned by root (service runs as root; config dir owned by x3).

### Live topology (ports / units)

```
:18080  freebuff-unified.service (root, x3)   ← unified gateway, front door
:3457   freebuff-proxy.service (freebuff user) ← upstream Go proxy (stealth)
:3101   hermes-sidecar.service (root)          ← Node TLS-fingerprint sidecar
:9091   dashboard of unified gateway (0.0.0.0!)
:31000  freebuff2api.service (Python/FastAPI)  ← separate Python impl
:18432  freebuff2api-admin.service
:8091   python (unidentified, loopback)
```

Chain: clients → :18080 (auth/limits/stealth) → front-door `/v1/*` passthrough →
:3457 → codebuff.com. Plus an entirely separate Python stack on :31000.

---

## 2. Code architecture of `freebuff-unified`

- Entry: `cmd/freebuff/main.go` (~600 lines): `serve|login|logout|check`.
- `internal/` — 13 packages, ~9.5k LOC (excluding deps):
  httpapi (Fiber v3 app), session (manager+pool), freebuff (upstream client),
  stealth (hermes roundtripper, SOCKS5 pool, refresher, metrics, TLS profiles),
  hermes (sidecar client), dashboard (probe engine+SSE+HTML), parallel (Parallel
  Web APIs), websearch (keyless DDG), config (+fsnotify hot reload), credentials
  (flock store), oauth, anthropic (mapper), openai (types), cache (run cache).
- Merges three lineages per SESSION_SUMMARY.md: freebuff-unified (auth/limits/
  breaker), freebuff-proxy (stealth/session/token pool), ai-stack (status).
- Front-door mode: `proxy.enabled: true` → `/v1/*` transparently forwarded to
  :3457 backend; gateway also has its own native chat path (session manager +
  upstream client) — **two overlapping completion paths**.

### Performance findings already documented in `planning/2026-09-09-freebuff-slow-investigation.md`

Root causes ranked: (1) buffered non-stream via hermes sidecar (110s cap < 5m infra
abort, no keep-alive), (2) hermes single-threaded Node bottleneck shared with the
SOCKS5 refresher's 150-candidate probing cycles, (3) blocking/sync health probes on
hot path + per-request credential disk reads, (4) Bun CLI cold start 1.5s,
(5) no deadline propagation. Fixes staged: immediate (streaming default, 280s
timeouts, early flush + 15s keep-alive comments, Fiber timeouts, async boot health),
short (cred/session caches, refresher isolation), medium (daemon mode).

Cross-check vs code: main.go already shows `stealthUpstreamTimeoutMS = 280_000`,
async boot health check, cached `probeBackend` (5s TTL), and session prewarm —
several "immediate" fixes have landed. The checked-in systemd unit for hermes
matches `deps/hermes-service/service.js` (per-route 280s codebuff timeout).

---

## 3. Findings (risks & drift)

### Duplication
1. `deps-fb2api` ≡ `deps/freebuff2api` — identical clones of Quorinex/FreeBuff2API
   at a1c1035, and **neither is imported by the unified Go module** (no import path
   reference; module name is a separate module). Pure dead weight (~370K each).
2. Two `freebuff-unified` binaries installed under x3 (`bin/freebuff` misnamed, plus
   current), plus a third deployment under x1 with its own config — version skew
   risk (x1 build is unknown vintage).
3. Two parallel implementations of the same product exist on this host: the Go
   unified gateway (:18080) and the Python Freebuff2API-Optimized (:31000), each
   with its own admin panel — overlapping function (both translate OpenAI/Anthropic
   to codebuff upstream).
4. `config.yaml` defines `server.api_keys` **and** `auth.api_keys` with the same two
   keys — config merge drift from the three-lineage merge.
5. `bin/freebuff-unified.new` leftover.

### Security
6. Dashboard binds `*:9091` (all interfaces) while gateway auth keys and an
   `auths/credentials.json` with token material sit in the repo; no `API_KEYS` gate
   on the dashboard itself. Gateway also binds `0.0.0.0:18080`.
7. `freebuff-unified.service` runs as **root** (CAP_NET_BIND_SERVICE note suggests
   privileged bind is the reason; ports 18080/9091 don't need root).
8. 338-entry `proxies.txt` at repo root is not referenced by config (inline
   `us_proxies` only); fetched via `scripts/fetch-proxies.sh` (public SOCKS5
   scrapes — untrusted egress IPs).
9. Real API keys committed in `config.yaml` (`fbu_*`) and SESSION_SUMMARY.md
   (nvapi-...). With no git history yet this is the last chance to keep them out.

### Hygiene
10. **No git commits, no .gitignore** in freebuff-unified: `bin/` (69MB of ELFs),
    `node_modules`, `auths/`, proxies.txt would all be committed as-is.
11. SESSION_SUMMARY.md references `internal/gateway/keypool.go` and
    `internal/gateway/ratelimit.go` — packages that don't exist (drift; breaker/
    limits now live elsewhere or were absorbed).
12. `deploy/docker/` is empty; docker-compose lives only in the unrelated
    obsidian-llm-wiki repo.
13. Config duplication: `stealth.us_proxies` inline (12) vs `proxies.txt` (338)
    vs refresher's own candidate fetch — three sources of egress truth.

---

## 4. Unification recommendations

### A. Repo hygiene (do first — cheap, unlocks safe versioning)
- Add `.gitignore`: `bin/`, `deps/hermes-service/node_modules/`, `auths/`,
  `proxies.txt`, `*.new`, `__pycache__/`, `.env`.
- Make the first commit **before** anything else so secrets scrubbing has a baseline.
- Scrub `fbu_*`/`nvapi-*` from config.yaml/SESSION_SUMMARY.md into `.env` +
  `{env:}`-style substitution or docs placeholders.
- Delete `bin/freebuff-unified.new`; rename `bin/freebuff` or remove (it's a stale
  unified build, not the upstream proxy).
- Replace `deps-fb2api/` + `deps/freebuff2api/` with a single `docs/vendored.md`
  note (or keep one clone under `deps/` only if it will actually be imported;
  today it never is). Saves 370K and removes provenance confusion.

### B. Converge the runtime (one gateway, one stack)
- Pick the survivor: the Go unified gateway. Then either
  - retire `freebuff2api.service`/`-admin` (Python :31000/:18432) if unused, or
  - explicitly declare it out of scope in SESSION_SUMMARY (it currently pretends
    to be part of ai-stack status but is a separate impl).
- Decide front-door vs native: `proxy.enabled=true` makes :18080 a passthrough to
  :3457, while the gateway also has a native completion path — pick one
  completion path or document why both exist.
- Fix x1 vs x3 skew: either redeploy x1 from a tagged build of x3 or document the
  x1 deployment as independent.
- Service hardening: run `freebuff-unified.service` as `x3` (or dedicated user)
  instead of root; bind dashboard to 127.0.0.1; put `API_KEYS` requirement on any
  non-loopback bind; drop `CAP_NET_BIND_SERVICE` if ports stay >1024.

### C. Finish the perf plan (per the 2026-09-09 investigation doc)
- Remaining immediate items: non-stream keep-alive heartbeats, Fiber
  Read/Write/Idle timeouts, streaming default.
- Short-term: credential/session caches, refresher isolation from hermes
  (direct SOCKS5 dial or second sidecar), geo lookup off hot path.
- Add the missing `internal/gateway` circuit breaker/rate limiter back or update
  SESSION_SUMMARY to match reality (config `limits:`/`auth.breaker:` currently
  parse but — verify — may not be enforced in httpapi; the middleware.go should
  be checked for enforcement).

### D. Verification commands (post-change)
```
opencode debug config >/dev/null && echo config-ok
curl -s http://127.0.0.1:18080/healthz | python3 -m json.tool | head -20
curl -s http://127.0.0.1:3457/healthz >/dev/null && echo :3457-ok
curl -s http://127.0.0.1:3101/healthz && echo
systemctl --no-pager status freebuff-unified hermes-sidecar freebuff-proxy | head -30
go build ./... && go test ./internal/...
```

---

## 5. Suggested next steps
1. Hygiene commit: .gitignore + secret scrub + delete .new + dedupe fb2api deps.
2. Decide survivor stack and document the topology decision in SESSION_SUMMARY.
3. Finish the perf-plan immediate items and re-benchmark per the investigation doc.

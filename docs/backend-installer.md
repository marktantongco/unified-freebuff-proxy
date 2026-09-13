# Unified Freebuff Proxy

**One command turns any Linux or macOS machine into a private, OpenAI-compatible AI gateway serving FreeBuff's free coding models — GLM 5.3 Flash, DeepSeek V4 Flash, GPT-5.6 Luna, MiMo 2.5 and more — with a web dashboard, account pool, and built-in anti-ban stealth.**

> This repo packages the excellent [trefeon/freebuff-proxy](https://github.com/trefeon/freebuff-proxy) (MIT) with a beginner-friendly installer, checksum verification, config seeding, and a health-checked first launch. Your tokens never appear in this repo.

---

## 🎁 What's in it for you

| You get | Why it matters |
|---|---|
| **Free frontier-class coding models** | GLM 5.3 Flash, DeepSeek V4 Flash and MiMo 2.5 are **unmetered** upstream — no per-model daily cap. Premium models (GPT-5.6 Luna, Muse Spark 1.3) share a 5/day pool. |
| **One endpoint for every AI tool** | Any tool that speaks the OpenAI API (Cursor, Cline, Continue, OpenCode, Aider, LibreChat, Chatbox, your own scripts) plugs in with just `base_url + model`. Anthropic-shape clients work too (`/v1/messages`). |
| **Zero compile, zero dependencies** | The installer downloads a prebuilt binary and verifies its SHA-256. No Go toolchain, no Node, no Docker needed. |
| **Token pool with failover** | Configure several FreeBuff accounts; requests ride out per-account daily quotas automatically (hot-session-first, cooldown on 429). |
| **Bridge mode for teams** | Leave the pool empty and every client brings its own token — one shared gateway, many accounts, isolated state per client. |
| **Built-in anti-ban stealth** | CLI-faithful TLS fingerprinting, sanitized headers, request jitter and idle rotation are on by default (`SAFE_MODE=true`). |
| **Web dashboard** | Live token health, quotas, served models, logs and a chat tester at `http://127.0.0.1:3457/admin` — no extra services. |
| **Deep reasoning by default** | `z-ai/glm-5.3-flash` is the default model: 1M-token context, images, reasoning ladder (low/high/max), $0 cost. |
| **Updatable in one command** | `-update` self-updater with SHA-256 verification built into the binary. |

---

## What this actually is

```
Your AI tool (Cursor / Cline / OpenCode / curl)          FreeBuff upstream
        │  standard OpenAI request                        (free models)
        ▼                                                        ▲
  http://127.0.0.1:3457/v1/chat/completions                      │
        │                                          CLI envelope + stealth egress
        └──► unified freebuff proxy ──────────────────────────────┘
             • picks a healthy token from the pool
             • manages the session lifecycle
             • streams the answer back as OpenAI SSE
```

The proxy translates standard OpenAI/Anthropic requests into FreeBuff's CLI session protocol, pools your account tokens, and makes egress look like the official CLI. It is a local adapter — your models, your accounts, your machine.

### Models you can serve

| Category | Model | Wire ID | Notes |
|---|---|---|---|
| Unlimited | **GLM 5.3 Flash** ⭐ default | `z-ai/glm-5.3-flash` | Deep reasoning, images, 1M context. Unmetered. |
| Unlimited | DeepSeek V4 Flash | `deepseek/deepseek-v4-flash` | Smart & fast, reasoning high. Unmetered. |
| Unlimited | MiMo 2.5 | `mimo/mimo-v2.5` | Balanced, images. Works on every tier. |
| Unlimited | Solar Pro 4 | `upstage/solar-pro4` | BYOK (Upstage), text-only, 500K context. |
| Premium | GPT-5.6 Luna | `openai/gpt-5.6-luna` | Shares the 5/day premium pool. |
| Premium | Muse Spark 1.3 | `meta/muse-spark-1.3-contributor` | Shared pool; queues then answers on V4 Flash. Meta trains on prompts. |
| Referral | GLM 5.2 | `z-ai/glm-5.2` | Referral-gated (+1/day promo). The proxy guards unentitled accounts to prevent upstream bans. |

Model availability is resolved **per account and per region at runtime** — `GET /v1/models` shows what your account actually gets.

---

## Requirements

- Linux (amd64/arm64) or macOS (amd64/arm64) — Windows: download the release ZIP from upstream manually
- `curl` and `tar` (preinstalled almost everywhere)
- A **FreeBuff/Codebuff account** with a login token (see Step 2 below)
- ~40 MB disk space

---

## 🐣 Beginner installation guide

### Step 0 — What you'll end up with

- A binary at `~/.local/bin/freebuff-proxy`
- A config at `~/.config/freebuff-proxy/.env` (holds your secret token, mode 600)
- A running gateway at `http://127.0.0.1:3457/v1`
- A dashboard at `http://127.0.0.1:3457/admin`

### Step 1 — Run the installer

```bash
curl -fsSL https://raw.githubusercontent.com/marktantongco/unified-freebuff-proxy/main/scripts/install.sh | bash
```

Prefer to inspect before running (recommended habit):

```bash
git clone https://github.com/marktantongco/unified-freebuff-proxy.git
cd unified-freebuff-proxy
./scripts/install.sh
```

Useful flags:

```bash
./scripts/install.sh --port 4000        # pick your own port
./scripts/install.sh --token cb_xxx     # pin a token instead of auto-discovery
./scripts/install.sh --no-start         # install only, don't launch
./scripts/install.sh --prefix ~/fp      # sandbox the whole install into one dir
```

### Step 2 — Get a FreeBuff token (if you don't have one)

1. Create a free account at [codebuff.com](https://www.codebuff.com) / [freebuff.com](https://freebuff.com) (use a real email — disposable domains are banned upstream).
2. Log in with the official CLI once: `npx codebuff@latest` (or your platform's installer) — this writes `~/.config/manicode/credentials.json`.
3. That's it. The installer leaves `AUTH_TOKENS` empty and **the proxy auto-discovers that CLI login at startup**. To pin tokens explicitly, edit `.env`:

```
AUTH_TOKENS=token1,token2,token3
```

then reload without restarting:

```bash
curl -X POST "http://127.0.0.1:3457/admin/reload" \
     -H "Authorization: Bearer <ADMIN_TOKEN from .env>"
```

### Step 3 — Verify it works

```bash
curl http://127.0.0.1:3457/healthz
```

Then send your first chat:

```bash
curl http://127.0.0.1:3457/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"z-ai/glm-5.3-flash","messages":[{"role":"user","content":"Say READY"}]}'
```

You should get back `"content":"READY"` — GLM is live.

### Step 4 — Open the dashboard

Visit `http://127.0.0.1:3457/admin` and sign in with the `ADMIN_TOKEN` value from `~/.config/freebuff-proxy/.env`. You'll see token health, live quotas, served models and logs.

### Step 5 — Connect your AI tool

| Tool | Setting |
|---|---|
| **Cursor** | Settings → Models → OpenAI API key: any string · Base URL: `http://127.0.0.1:3457/v1` · Model: `z-ai/glm-5.3-flash` |
| **Cline / Continue (VS Code)** | Provider: OpenAI Compatible · Base URL: `http://127.0.0.1:3457/v1` · API key: any string · Model: `z-ai/glm-5.3-flash` |
| **OpenCode** | Add provider with base URL `http://127.0.0.1:3457/v1`, api key `sk-anything` |
| **Aider** | `aider --openai-api-base http://127.0.0.1:3457/v1 --openai-api-key sk-anything --model openai/z-ai/glm-5.3-flash` |
| **curl / scripts** | The command from Step 3 |

API keys: unless you configure `API_KEYS`, local calls need **no real key** — any placeholder works. Set `API_KEYS=key1,key2` in `.env` to require them.

---

## Token modes

| Mode | How | Best for |
|---|---|---|
| **Pooled** (recommended for personal use) | `AUTH_TOKENS=tok1,tok2` in `.env` | One user, several accounts, maximum uptime — requests fail over when a token hits its daily quota. |
| **Auto-discovery** | Leave `AUTH_TOKENS=` empty; the proxy reads your CLI login at startup | Zero-config personal use after a single CLI login. |
| **Bridge** | `AUTH_TOKENS=` empty **and** `AUTO_DISCOVER_TOKEN=false` in `.env` | A shared router serving many people, each sending their own token as `Authorization: Bearer <token>`. |
| **Hybrid** (default) | `AUTH_TOKENS` set + `BRIDGE_ENABLED=true` | Pool serves key-less local clients; foreign credentials are relayed per-client. |

---

## Configuration cheat sheet

Config lives at `~/.config/freebuff-proxy/.env` (or `./.env` next to the binary — that wins). Full reference: upstream [`docs/`](https://github.com/trefeon/freebuff-proxy/tree/main/docs) and `.env.example` shipped with the release.

| Key | Default | Meaning |
|---|---|---|
| `LISTEN_ADDR` | `127.0.0.1:3457` | Where the gateway listens. Bind `0.0.0.0` only behind a firewall. |
| `AUTH_TOKENS` | *(empty)* | Comma-separated token pool. Empty = auto-discover / bridge. |
| `API_KEYS` | *(empty)* | Client gate keys. Required if you bind non-loopback. |
| `ADMIN_TOKEN` | *(generated)* | Dashboard + `/admin/reload` auth. Change it if you didn't install via this script. |
| `SAFE_MODE` | `true` | Anti-ban presets: TLS stealth, header sanitization, jitter, idle rotation. Keep on. |
| `COST_MODE` | `free` | Upstream billing mode. Leave `free`. |
| `QUOTA_FALLBACK_MODELS` | `glm-5.2→flash`, etc. | When a model's quota is exhausted/unentitled, degrade to the mapped model instead of erroring. |
| `MODEL_LOCKS` | *(off)* | Pin pool slots to models, e.g. `0:z-ai/glm-5.2;1:deepseek/deepseek-v4-flash`. |
| `MODELS_ALLOW` | *(all)* | Allowlist of served model IDs. |
| `MODELS_HIDE_UNAVAILABLE` | `false` | Prune unavailable models from `/v1/models`. |

Apply changes with `POST /admin/reload` (hot) or restart the process.

---

## About "activating GLM"

Two different GLMs exist upstream and it's worth knowing the difference:

- **GLM 5.3 Flash** (`z-ai/glm-5.3-flash`) is the **default** — served out of the box, unmetered, no activation needed. This is what the installer configures.
- **GLM 5.2** (`z-ai/glm-5.2`) is **referral-gated** (+1/day promo pool). Requesting it with an unentitled account upstream can hard-ban the account, so the proxy probes entitlement first and refuses locally (or falls back to V4 Flash per `QUOTA_FALLBACK_MODELS`). To use it, earn the referral perk on your account, then request the model normally.

**Region reality check:** FreeBuff assigns your access tier from the TCP source IP at Cloudflare's edge. Datacenter/VPN egress (and non-Tier-1 residential IPs) get the *limited* tier, where upstream coerces every model request to MiMo 2.5 — no proxy setting can override that. The fix is network-level: run the proxy from a Tier-1 residential connection (e.g. via a Tailscale exit node on a home machine). See upstream docs: *Workarounds for limited-tier IPs*.

---

## ⚠️ Terms-of-service and safety

Using your FreeBuff token through any third-party client **conflicts with FreeBuff/Codebuff ToS**. Upstream runs per-IP scoring, per-account trust levels, daily spend ceilings and farm-shape sweeps; accounts can be suspended or permanently banned. This proxy reduces (not eliminates) that risk.

Do / don't (evidence-backed, from upstream docs):

| ✅ Do | ❌ Don't |
|---|---|
| Keep `SAFE_MODE=true` | Run unattended 24/7 heavy automation |
| Use a normal residential connection | Use VPN/proxy/Tor or datacenter egress |
| Register with a real email | Use temp-mail (documented ban cohort) |
| Drain one token until it's rate-limited | Rotate many healthy keys (farming signal) |
| Read 429 as quota (resets Pacific midnight) | Confuse 429 with a ban (403 = terminal) |
| Request models your tier offers | Hammer out-of-region models |

---

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `address already in use` on start | Port taken (3457 is popular). Installer picks a free port; or set `LISTEN_ADDR`. Check `ss -tlnp \| grep 3457`. |
| `/healthz` never turns healthy | Read the log: `~/.local/state/freebuff-proxy.log`. Common: invalid token, no network, upstream outage. |
| Every model answers as MiMo | Your IP is limited-tier — upstream coerces all models to MiMo. See *About "activating GLM"* above. |
| `401 unauthorized` | `API_KEYS` is set — send `Authorization: Bearer <key>`, or unset `API_KEYS` for local-only use. |
| 429 responses | Token quota exhausted (resets Pacific midnight). Add tokens to the pool or wait for reset. |
| 403 `banned` / `country_blocked` | Account is gone upstream — register a fresh established account (real email, residential IP). |
| GLM 5.2 returns 429 `no referral quota` | Working as designed — the account lacks the referral perk; the proxy is protecting it from a ban. |
| Dashboard won't load | Confirm `DASHBOARD_ENABLED=true` and use the exact `ADMIN_TOKEN` from `.env`. |
| Config edits do nothing | POST `/admin/reload`, or restart the process. `.env` next to the binary wins over the platform path. |

Diagnostics built into the binary:

```bash
~/.local/bin/freebuff-proxy -doctor          # config, port, DNS/TLS, registry, region
~/.local/bin/freebuff-proxy -test-token      # zero-cost probe of the first token
~/.local/bin/freebuff-proxy -validate-tokens # health report for the whole pool
~/.local/bin/freebuff-proxy -update          # self-update with checksum verification
```

---

## Uninstall

```bash
./scripts/uninstall.sh                 # stop + remove binary, keep config
./scripts/uninstall.sh --purge-config  # also delete the .env (secrets!)
```

Logs stay at `~/.local/state/freebuff-proxy.log` — delete manually if you want.

---

## FAQ

**Is the model output really free?** Yes — upstream FreeBuff free-tier sessions fund it; `usage.cost` in responses reads `0`. Premium-pool models consume your account's 5/day shared allowance.

**Does this work on Windows?** Not via this installer yet. Grab `freebuff-proxy_*_windows_amd64.zip` from [upstream releases](https://github.com/trefeon/freebuff-proxy/releases), run `start-proxy.cmd`, and follow Step 2 onward.

**Where do my tokens live?** Only in `~/.config/freebuff-proxy/.env` (chmod 600) or the `.env` you point at. The installer never transmits them anywhere; this repo contains none.

**Can I expose this to my LAN/team?** Set `LISTEN_ADDR=0.0.0.0:3457`, set `API_KEYS`, put a reverse proxy with TLS in front, and read the upstream *Deployment* docs. Understand the ToS risk first — pooled accounts behind many users is exactly the pattern upstream watches for.

**How is this different from running trefeon/freebuff-proxy directly?** Functionally identical — same binary, verified checksums. This repo adds the guided installer, config seeding with sane defaults, health-checked launch, and beginner docs.

**Why "unified"?** One gateway serves OpenAI-shape (`/v1/chat/completions`, `/v1/responses`) and Anthropic-shape (`/v1/messages`) clients against the same pool — no per-protocol duplication.

---

## Credits & license

- Proxy, dashboard and upstream protocol implementation: **[trefeon/freebuff-proxy](https://github.com/trefeon/freebuff-proxy)** — MIT License. This repo ships its release binaries unmodified.
- Installer, packaging and docs: **[marktantongco/unified-freebuff-proxy](https://github.com/marktantongco/unified-freebuff-proxy)** — MIT License.
- FreeBuff/Codebuff upstream and its models are the property of their respective owners; this project is not affiliated with them.

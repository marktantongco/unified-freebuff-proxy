# 2026-09-10 — cdp-proxy-interceptor integration (+ autoclaw-autologin sync, stealth assessment)

## Request
`install and integrate with puppeteer or playwright https://github.com/zackiles/cdp-proxy-interceptor`
(preceded by `andreanocalvin/autoclaw-autologin` + `tholian-network/stealth` batch-headless requests).

## Inspect-first verdicts

| Repo | Verdict | Notes |
|---|---|---|
| `zackiles/cdp-proxy-interceptor` | **Clean, installed** | MIT, Deno 2.1.4-pinned, active upstream. CDP MITM proxy: playwright/puppeteer connect to it; it owns Chrome and can intercept/modify CDP messages via plugins. |
| `andreanocalvin/autoclaw-autologin` | **Clean, synced** | Same project as local `~/aiworkspace/autoclaw-autologin` (only endpoint: `autoglm-api.autoglm.ai`; b64 hits are JWT parsing). Local was *ahead* on the login engine (autolib delegation) — synced only non-conflicting improvements. |
| `tholian-network/stealth` | **Clean, built, not integrable as login engine** | GPL-3 hardened browser. Builds + serves on `:65432`, but exposes **no CDP server and no playwright API** — cannot drive Google OAuth form automation. Left at `~/aiworkspace/stealth` as a scraper/proxy option. |

## Installed: cdp-proxy-interceptor

- Location: `~/aiworkspace/cdp-proxy-interceptor` (Deno 2.1.4 at `~/.deno/deno`)
- Service: `cdp-proxy-interceptor.service` — active, enabled, `Restart=on-failure`
- Endpoint: **`127.0.0.1:9222` (loopback-only** — added `hostname` to `Deno.serve` in `src/main.ts`; upstream defaults to `0.0.0.0`)
- Chrome: reuses existing `~/.cache/ulixee/chrome/139.0.7258.154/chrome` via `.env` `CHROMIUM_EXECUTABLE_PATH` (no 150MB download)
- Hardening added: `--disable-blink-features=AutomationControlled` in `CHROME_FLAGS` (`src/constants.ts`) → **`navigator.webdriver === false`** for clients

### Upstream bug found (documented, not fixed)
`plugins/playwright-stealth-plugin.disabled.ts` (ships disabled upstream) cannot work as authored:
1. It exports an **instance**; `main.ts` expects a **class** (`typeof === 'function'`) → silent no-register. (Fixable: `export default RuntimeEnableMitMPlugin`.)
2. Fatal: `onRequest` passes the **CDP sessionId** to `emitClientEvent`, which expects the proxy's **own session UUID** → `Invalid proxy session ID` → client "Target closed". The plugin API (`onRequest(request)` sees only raw CDP messages) provides no proxy-session handle, so it's unfixable **within the plugin API** — needs an upstream API change (e.g., pass a session context to plugin hooks). Left disabled; reported via this doc.

## Integration: autolib `cdp` engine

`~/aiworkspace/invisible-automation/autolib/engines.py` now supports `engine="cdp"`:
- Discovers the proxy via `CDP_PROXY_ENDPOINT` / `CDP_PROXY_URL` env or TCP probe of `CDP_PROXY_PORT` (default 9222)
- `launch_browser(engine="cdp")` → `connect_over_cdp` → stealth-flagged Chromium owned by the service
- Engine order preference stays `invisible` (patched Firefox) > `cloak` > `cdp`; batch jobs opt in with `--engine cdp`

Verified end-to-end: `autolib cdp engine -> Example Domain | webdriver: False`.

## autoclaw-autologin local sync (non-conflicting only)

- `ui/index.html` dashboard synced (proxy.py already served the route; was 404 locally → now live at `:31000/`)
- `config.py` replaced with upstream (adds `host:port` proxy-list parsing; local gap) — backup at `config.py.bak-upstream-sync`
- NOT overwritten: `autoclaw_autologin.py` (local delegates to autolib — newer than repo's direct-cloakbrowser version), `stealth_login.py` (local-only), `proxy.py`/`auth.py`/`login.py` (identical)
- `autoclaw-proxy.service` restarted; `:31000` healthy; token store still **0 accounts** (login still requires interactive Google OAuth — unchanged blocker for :31000 chat)

## Usage

```bash
# Playwright / puppeteer via the proxy (puppeteer: puppeteer.connect({browserURL: 'http://127.0.0.1:9222'}))
playwright.chromium.connect_over_cdp("ws://127.0.0.1:9222/devtools/browser")

# autolib batch engine
python stealth_login.py --engine cdp ...   # or engine="cdp" in autolib calls
```

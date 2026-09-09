# Freebuff2API-Optimized — install/integrate request outcome — 2026-09-09

Request: "install and integrate https://github.com/kele68108/Freebuff2API-Optimized"

## Verdict

**Legitimate — and already deployed here.** The running
`freebuff2api.service` (/opt/freebuff/python/Freebuff2API-Optimized,
gateway :20004, admin :20003) is an unversioned snapshot of exactly this
project (main.py + exploitation.js byte-identical, same admin layout).
"Install" therefore meant **upgrade to repo HEAD (f7e13d4)**.

## Provenance evidence

- AGPL-3.0 LICENSE; single coherent author (kele68108) across ~10 commits
  with feature/fix messages matching real code.
- Suspicious-pattern scan (py/sh/js): clean.
- Root file `exploitation.js` is a Cloudflare Worker that reverse-proxies
  codebuff.com (strips CF/IP headers, rewrites cookies/links, drops CSP).
  Not malware; deploying it publicly would be phishing-shaped — we did not.
- Unlike the refused Simmondsbrightasanewpenny643/freebuff-proxy fork, no
  deleted security infra, no binary droppers, no social-engineering README.

## Upgrade performed

1. Staged repo HEAD at /opt/freebuff/python/.staging-fb2api.
2. `uv sync --frozen` (lock-exact venv) + repo test suite: **51/51 passed**.
3. Full backup (excl. venvs): /opt/freebuff/python/Freebuff2API-Optimized.bak-20260909.tar.gz.
4. rsync overlay preserving: .env, .venv, admin/venv; verified the 2
   local-only files (cli_prompt.py, metrics.py) survived (no --delete).
5. Restart: gateway + admin active; models endpoint OK with configured key.
6. Admin crash-loop fix: on-disk unit sets FREEBUFF2API_DATA_DIR=/opt/freebuff/.freebuff2api
   but systemd ran a stale loaded unit; daemon-reload + mkdir/chown + restart → 200.

## Notes

- Chat test hit upstream 429 (accessTier limited, pool freebucks, resets
  Pacific midnight) — upstream quota semantics, not a code regression.
- This gateway's model allowlist excludes GLM; DeepSeek/Kimi/MiniMax served.
- Rollback: stop services, restore tarball, restart.
- Repo pytest is not in uv.lock; needed `uv pip install pytest` in staging.

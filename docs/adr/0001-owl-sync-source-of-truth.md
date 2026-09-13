# ADR 0001 — Single-source-of-truth sync for owl-agent-stack

**Status**: Accepted (2026-09-14)

## Context

`owl-agent-stack` lives as 3 separate copies on the host:

| Path | Owner | Source |
|---|---|---|
| `/home/x1/.owl-agent/` | x1 | running instance (`owl-agent.service` cwd) |
| `/root/.owl-agent/` | root | legacy systemd ExecStart path |
| `/home/x1/owl-agent-stack/` | x1 | git checkout, source-of-truth reference |

Each had drifted from the others by file md5 (`owl_server.py`, `proxy_defense.py`) and was missing 3 newer modules (`chameleon_ai.py`, `forward_proxy.py`, `owl_resilient_mcp.py`).

## Decision

Adopt `~/workspace/unified-owl/` (6-repo merged ecosystem, MIT, freshest upstream) as the **single source of truth**. Two helper binaries:

- `~/.local/bin/owl-sync` — idempotent md5 diff + sync to x1 + root targets via sudo
- `~/.local/bin/owl-watch` — systemd user unit, polls every 30s, auto-runs `owl-sync --restart` on drift

## Consequences

- All future owl-server edits land in `~/workspace/unified-owl/`; watcher propagates
- Service restarts on every drift (kills old PID, frees port, restart-failed, daemon-reload, start)
- 3 places stay byte-identical, drift impossible to ignore
- Backup: each script writes to `~/.local/share/owl-{sync,watch}.log`

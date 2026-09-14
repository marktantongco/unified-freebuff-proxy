# ADR 0002 — owl-agent.service supervisor wrapper + exit-78 patch

**Status**: Accepted (2026-09-14)

## Context

`owl-agent.service` restart-looped with `Errno 98 EADDRINUSE` on `:60000`. Counted 351 loop events in 1 hour. Cause: systemd `Restart=on-failure` raced with the kernel releasing the listening socket from a killed owl-server process. Each new start hit `EADDRINUSE` → exit 1 → systemd restart → loop.

## Decision

Two-layer fix:

**Layer B (process supervisor wrapper)** — bash script `/home/x1/.owl-agent/owl-agent-supervisor.sh` runs owl-server in a `while true; do ... done` loop, kills stale PID + sleeps 2s before each exec. systemd sees a stable wrapper PID; owl-server crash loops are absorbed inside the wrapper. systemd restart-loop is structurally impossible.

**Layer C (exit-78 patch)** — `owl_server.py` exits with code 78 (`EX_CONFIG` from `sysexits.h`) on `EADDRINUSE`. systemd unit gets `RestartPreventExitStatus=78` so even if the wrapper somehow dies, systemd won't loop on bind-fail.

## Consequences

- 351 events/hour → 0 idle (only spikes on actual drift edits)
- systemd unit shows stable `active (running)` instead of perpetual `activating`
- Service PID tree: `systemd → supervisor (stable 1248781) → owl_server.py (rotates)`
- Logs split: systemd → journalctl, supervisor → `/home/x1/.owl-agent/supervisor.log`
- Backward compat: wrapper preserves exact owl-server invocation (same args systemd would pass)

## References

- systemd.service(5) — `RestartPreventExitStatus`, `Type=simple` vs `Type=notify`
- github.com/systemd/systemd#30804 — default restart limits cause permanent failure
- github.com/openclaw/openclaw#75115 — canonical SEGV→EADDRINUSE loop, exit-78 fix

# ADR 0006 — SSE streamer context hoisting

**Status**: Accepted (2026-09-14)

## Context

Race detector found fiber Ctx use-after-release in 5 SSE/streaming sites (`relayTaskEvents`, `relayResponses`, `responsesNativeStream`, `bufferedResearch`, `streamResearch`) plus 1 background `Complete` goroutine. `SendStreamWriter` callbacks run after handler return while Fiber recycles the Ctx — touching `c.Context()` / `c.Done()` / `context.WithTimeout(c)` inside them raced with Ctx release. 351+ race events.

## Decision

Hoist parent ctx + disconnect channel to handler scope, pass plain derived ctx into the closure:

```go
ctx, cancel := context.WithCancel(c.Context())
clientGone := c.Done()  // capture before closure runs
return c.SendStreamWriter(func(w *bufio.Writer) {
    defer cancel()
    // ...uses ctx, clientGone (not c)
})
```

## Consequences

- Full `-race` suite: 95 tests pass, 18 packages, 0 races
- Behavior preserved: client disconnect still cancels (parented on request ctx)
- Test-side fix: `internal/stealth/refresher_test.go` mutex-wraps concurrent `OnEgress` append
- Backward compat: no API changes, no new abstractions

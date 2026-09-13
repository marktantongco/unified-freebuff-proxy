package stealth

import (
	"sync"
	"time"
)

// Metrics records stealth-transport observations for /stealth/status:
// sidecar latency, fallback (bypass/fail-open) counts, and egress IPs used.
// All methods are safe for concurrent use.
type Metrics struct {
	mu              sync.Mutex
	startedAt       time.Time
	sidecarRequests int64
	sidecarErrors   int64
	fallbacks       int64 // SSE bypasses + fail-open reroutes
	totalLatencyMS  int64
	lastLatencyMS   int64
	maxLatencyMS    int64
	egressIPs       map[string]int64 // ip -> count (bounded)
	lastEgressIP    string
	lastError       string
}

// MaxEgressIPs bounds memory; the most-recently-seen IPs beyond this are
// dropped (counts are approximate for evicted entries).
const MaxEgressIPs = 64

// NewMetrics returns a metrics tracker starting its clock now.
func NewMetrics() *Metrics {
	return &Metrics{
		startedAt: time.Now(),
		egressIPs: make(map[string]int64),
	}
}

// RecordSidecar notes one sidecar-routed request and its latency.
func (m *Metrics) RecordSidecar(latency time.Duration) {
	if m == nil {
		return
	}
	ms := latency.Milliseconds()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sidecarRequests++
	m.totalLatencyMS += ms
	m.lastLatencyMS = ms
	if ms > m.maxLatencyMS {
		m.maxLatencyMS = ms
	}
}

// RecordSidecarError notes a failed sidecar attempt.
func (m *Metrics) RecordSidecarError(err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sidecarErrors++
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		m.lastError = msg
	}
}

// RecordFallback notes a request that bypassed the sidecar (SSE or fail-open).
func (m *Metrics) RecordFallback() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallbacks++
}

// RecordEgress notes the egress IP observed for a proxied request.
func (m *Metrics) RecordEgress(ip string) {
	if m == nil || ip == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.egressIPs[ip]; !seen && len(m.egressIPs) >= MaxEgressIPs {
		return // bounded: skip tracking new IPs past the cap
	}
	m.egressIPs[ip]++
	m.lastEgressIP = ip
}

// Snapshot returns a copy of the counters for JSON serialization.
func (m *Metrics) Snapshot() map[string]any {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	avg := int64(0)
	if m.sidecarRequests > 0 {
		avg = m.totalLatencyMS / m.sidecarRequests
	}
	ips := make(map[string]int64, len(m.egressIPs))
	for k, v := range m.egressIPs {
		ips[k] = v
	}
	out := map[string]any{
		"uptime_s":         int64(time.Since(m.startedAt).Seconds()),
		"sidecar_requests": m.sidecarRequests,
		"sidecar_errors":   m.sidecarErrors,
		"fallbacks":        m.fallbacks,
		"latency_ms": map[string]int64{
			"last": m.lastLatencyMS,
			"avg":  avg,
			"max":  m.maxLatencyMS,
		},
		"egress_ips_used": len(ips),
		"last_egress_ip":  m.lastEgressIP,
	}
	if m.lastError != "" {
		out["last_sidecar_error"] = m.lastError
	}
	if len(ips) > 0 {
		out["egress_ips"] = ips
	}
	return out
}

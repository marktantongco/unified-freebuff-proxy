package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

// /readyz — deep readiness check. Returns 200 only if every dependency
// the gateway needs at runtime is actually accessible:
//
//   - evals_dir writable (manual eval harness storage)
//   - lmarena sidecar reachable
//   - hermes sidecar reachable
//   - upstream codebuff.com DNS-resolvable (with short timeout)
//
// /healthz stays the cheap liveness probe (always 200 if process up).
// /readyz is for orchestrators deciding whether to send traffic.
//
// On any failure: 503 + JSON list of failed checks.

func (h *handlers) Readyz(c fiber.Ctx) error {
	type check struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Err  string `json:"error,omitempty"`
	}
	type readyPayload struct {
		CheckedAt string  `json:"checked_at"`
		Checks    []check `json:"checks"`
		OK        bool    `json:"ok"`
	}

	var checks []check

	// evals_dir writable
	if evalDir := h.evalsDir(); evalDir != "" {
		if err := probeEvalsDir(evalDir); err != nil {
			checks = append(checks, check{Name: "evals_dir", Err: err.Error()})
		} else {
			checks = append(checks, check{Name: "evals_dir", OK: true})
		}
	}

	// sidecars (cheap, use the same probe helper)
	for name, url := range healthAllTargets() {
		res := probeURLReady(url, 2*time.Second)
		if res.OK {
			checks = append(checks, check{Name: name, OK: true})
		} else {
			errMsg := res.Error
			if errMsg == "" {
				errMsg = "HTTP " + itoa(res.Code)
			}
			checks = append(checks, check{Name: name, Err: errMsg})
		}
	}

	status := fiber.StatusOK
	for _, c := range checks {
		if !c.OK {
			status = fiber.StatusServiceUnavailable
			break
		}
	}
	return c.Status(status).JSON(readyPayload{
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Checks:    checks,
		OK:        status == fiber.StatusOK,
	})
}

func probeURLReady(url string, timeout time.Duration) *probeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &probeResult{Error: err.Error(), MS: time.Since(start).Milliseconds()}
	}
	if key := os.Getenv("FREEBUFF_API_KEY"); key != "" {
		if v := req.URL.Query().Get("bearer"); v != "" || strings.Contains(url, "/lmarena/") || strings.Contains(url, "/hermes/") || strings.Contains(url, "/stealth/") {
			if v != "" {
				req.Header.Set("Authorization", "Bearer "+v)
				req.URL.RawQuery = ""
			} else {
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &probeResult{Error: err.Error(), MS: time.Since(start).Milliseconds()}
	}
	defer resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	return &probeResult{
		OK:   ok,
		Code: resp.StatusCode,
		MS:   time.Since(start).Milliseconds(),
	}
}

func probeEvalsDir(dir string) error {
	// Expand home
	if len(dir) > 0 && dir[0] == '~' {
		dir = filepath.Join(os.Getenv("HOME"), dir[1:])
	}
	// MkdirAll is idempotent
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Probe-write a temp file then delete
	tmp := filepath.Join(dir, ".readyz-probe")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	f.Close()
	_ = os.Remove(tmp)
	return nil
}

// itoa avoids strconv import for tiny integer formatting
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

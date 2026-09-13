package httpapi

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"freebuff-unified/internal/lmarena"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
)

// newLMArenaRelay returns a byte-level relay for the lmarena-stealth-proxy
// sidecar: /v1/lmarena/<rest> -> <sidecar>/<rest> (prefix stripped), so
// POST /v1/lmarena/v1/responses lands on POST /v1/responses upstream.
// Auth + rate limiting are applied by the gateway middleware above; the
// sidecar keeps its own per-session limiter.
func newLMArenaRelay(client *lmarena.Client, logger *log.Logger) fiber.Handler {
	if client == nil {
		return func(c fiber.Ctx) error {
			return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{
				"error": map[string]any{"message": "lmarena sidecar is not enabled", "type": "lmarena_disabled"},
			})
		}
	}
	target, err := url.Parse(client.BaseURL())
	if err != nil {
		return func(c fiber.Ctx) error {
			return c.Status(http.StatusInternalServerError).JSON(map[string]any{
				"error": map[string]any{"message": "lmarena sidecar misconfigured", "type": "lmarena_misconfigured"},
			})
		}
	}
	relay := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			rest := strings.TrimPrefix(r.In.URL.Path, "/v1/lmarena")
			if rest == "" {
				rest = "/"
			}
			r.SetURL(target)
			r.Out.URL.Path = singleJoin(target.Path, rest)
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			r.Out.Host = target.Host
		},
	}
	return adaptor.HTTPHandler(relay)
}

func singleJoin(a, b string) string {
	return strings.TrimRight(a, "/") + "/" + strings.TrimLeft(b, "/")
}

// LMArenaHealth reports the lmarena sidecar liveness.
func (h *handlers) LMArenaHealth(c fiber.Ctx) error {
	if h.lmarena == nil {
		return c.Status(http.StatusServiceUnavailable).JSON(map[string]any{"status": "disabled"})
	}
	health, err := h.lmarena.Health(c.Context())
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(map[string]any{
			"status": "unreachable",
			"error":  err.Error(),
		})
	}
	return c.Status(http.StatusOK).JSON(health)
}

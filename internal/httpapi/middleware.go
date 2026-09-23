package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
)

const bearerPrefix = "Bearer "

// authMiddleware enforces the proxy API keys on all requests. Every configured
// key is accepted — not just the first one — so key rotation can add a new key
// before removing the old. Blank entries are dropped: an unset x-api-key header
// also reads as "", so an unfiltered empty entry would authenticate
// credential-less requests.
func authMiddleware(proxyAPIKeys []string) fiber.Handler {
	keys := make([]string, 0, len(proxyAPIKeys))
	for _, key := range proxyAPIKeys {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	return func(c fiber.Ctx) error {
		if !validAPIKey(c, keys) {
			return writeOpenAIError(c, http.StatusUnauthorized, "invalid_api_key", "A valid Bearer API key is required")
		}
		return c.Next()
	}
}

// validAPIKey reports whether the request carries one of the accepted keys, in
// either the Authorization: Bearer form or the x-api-key header form. Values are
// compared in constant time so a mismatch does not leak key material by timing.
func validAPIKey(c fiber.Ctx, keys []string) bool {
	authorization := c.Get(fiber.HeaderAuthorization)
	headerKey := c.Get("x-api-key")
	for _, key := range keys {
		if subtle.ConstantTimeCompare([]byte(authorization), []byte(bearerPrefix+key)) == 1 ||
			subtle.ConstantTimeCompare([]byte(headerKey), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

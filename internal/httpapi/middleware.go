package httpapi

import (
	"net/http"

	"github.com/gofiber/fiber/v3"
)

const bearerPrefix = "Bearer "

// authMiddleware enforces the proxy API key on all requests.
func authMiddleware(proxyAPIKey string) fiber.Handler {
	return func(c fiber.Ctx) error {
		if c.Get(fiber.HeaderAuthorization) != bearerPrefix+proxyAPIKey && c.Get("x-api-key") != proxyAPIKey {
			return writeOpenAIError(c, http.StatusUnauthorized, "invalid_api_key", "A valid Bearer API key is required")
		}
		return c.Next()
	}
}

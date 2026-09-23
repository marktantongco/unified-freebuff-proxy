package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// modelsRequest builds a GET /v1/models request with the given credentials.
func modelsRequest(t *testing.T, authorization, apiKeyHeader string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if authorization != "" {
		req.Header.Set(fiber.HeaderAuthorization, authorization)
	}
	if apiKeyHeader != "" {
		req.Header.Set("x-api-key", apiKeyHeader)
	}
	return req
}

// Every entry of server.api_keys must authenticate, not just the first one.
func TestAuthAcceptsEveryConfiguredKey(t *testing.T) {
	app := NewApp(Options{
		Model:        "m",
		ProxyAPIKeys: []string{"key-one", "key-two"},
		Chat:         okChatService{},
	})

	cases := []struct {
		name          string
		authorization string
		apiKeyHeader  string
	}{
		{"bearer first key", "Bearer key-one", ""},
		{"bearer second key", "Bearer key-two", ""},
		{"x-api-key second key", "", "key-two"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := doRequest(t, app, modelsRequest(t, tc.authorization, tc.apiKeyHeader))
			if status == http.StatusUnauthorized {
				t.Fatalf("configured key was rejected: status %d body %s", status, body)
			}
		})
	}
}

func TestAuthRejectsUnknownKeyWhenMultipleConfigured(t *testing.T) {
	app := NewApp(Options{
		Model:        "m",
		ProxyAPIKeys: []string{"key-one", "key-two"},
		Chat:         okChatService{},
	})

	status, _ := doRequest(t, app, modelsRequest(t, "Bearer key-three", ""))
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown key: status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// A blank entry must never authenticate: an unset x-api-key also reads as "",
// so an unfiltered empty key would let credential-less requests through.
func TestAuthIgnoresEmptyConfiguredKeys(t *testing.T) {
	app := NewApp(Options{
		Model:        "m",
		ProxyAPIKeys: []string{"", "key-one"},
		Chat:         okChatService{},
	})

	status, _ := doRequest(t, app, modelsRequest(t, "", ""))
	if status != http.StatusUnauthorized {
		t.Fatalf("credential-less request: status = %d, want %d", status, http.StatusUnauthorized)
	}

	status, _ = doRequest(t, app, modelsRequest(t, "Bearer key-one", ""))
	if status == http.StatusUnauthorized {
		t.Fatalf("non-empty key alongside a blank entry was rejected: status %d", status)
	}
}

// The single-key field must keep working for existing callers.
func TestAuthLegacySingleKeyStillWorks(t *testing.T) {
	app := NewApp(Options{Model: "m", ProxyAPIKey: "legacy-key", Chat: okChatService{}})

	status, _ := doRequest(t, app, modelsRequest(t, "Bearer legacy-key", ""))
	if status == http.StatusUnauthorized {
		t.Fatalf("legacy single key rejected: status %d", status)
	}

	status, _ = doRequest(t, app, modelsRequest(t, "Bearer other", ""))
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown key against legacy field: status = %d, want %d", status, http.StatusUnauthorized)
	}
}

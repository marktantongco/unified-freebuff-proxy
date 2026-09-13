package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"freebuff-unified/internal/credentials"
)

const (
	loginCodePath   = "/api/auth/cli/code"
	loginStatusPath = "/api/auth/cli/status"
	logoutPath      = "/api/auth/cli/logout"
)

var (
	errPendingStatus  = errors.New("Authentication failed")
	errOAuthTransport = errors.New("oauth transport error")
)

// Flow executes login/logout operations via the Freebuff CLI OAuth endpoints.
type Flow struct {
	BaseURL       string
	HTTPClient    *http.Client
	Store         credentials.Store
	FingerprintID func() (string, error)
	PollInterval  time.Duration
	PollTimeout   time.Duration
	Sleep         func(context.Context, time.Duration) error
}

// LoginCode contains the login URL and polling metadata.
type LoginCode struct {
	FingerprintID   string
	FingerprintHash string
	LoginURL        string
	ExpiresAt       string
}

type loginCodeResponse struct {
	FingerprintID   string          `json:"fingerprintId"`
	FingerprintHash string          `json:"fingerprintHash"`
	LoginURL        string          `json:"loginUrl"`
	ExpiresAt       json.RawMessage `json:"expiresAt"`
}

type loginStatusResponse struct {
	User  credentialResponse `json:"user"`
	Error string             `json:"error"`
}

type credentialResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Email           string `json:"email"`
	AuthToken       string `json:"authToken"`
	FingerprintID   string `json:"fingerprintId"`
	FingerprintHash string `json:"fingerprintHash"`
}

// RequestLoginCode gets a login URL from the Freebuff API.
func (f *Flow) RequestLoginCode(ctx context.Context) (LoginCode, error) {
	if f.FingerprintID == nil {
		fid, err := generateFingerprintID()
		if err != nil {
			return LoginCode{}, fmt.Errorf("generate fingerprint id: %w", err)
		}
		f.FingerprintID = func() (string, error) { return fid, nil }
	}

	fingerprintID, err := f.FingerprintID()
	if err != nil {
		return LoginCode{}, fmt.Errorf("get fingerprint id: %w", err)
	}

	payload := map[string]any{
		"fingerprintId": fingerprintID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return LoginCode{}, fmt.Errorf("marshal login code request: %w", err)
	}

	resp, err := f.post(ctx, loginCodePath, body)
	if err != nil {
		return LoginCode{}, fmt.Errorf("request login code: %w", err)
	}
	defer resp.Body.Close()

	var codeResp loginCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&codeResp); err != nil {
		return LoginCode{}, fmt.Errorf("decode login code response: %w", err)
	}

	return LoginCode{
		FingerprintID:   codeResp.FingerprintID,
		FingerprintHash: codeResp.FingerprintHash,
		LoginURL:        codeResp.LoginURL,
		ExpiresAt:       string(codeResp.ExpiresAt),
	}, nil
}

// PollLoginStatus polls the login status until authentication completes.
func (f *Flow) PollLoginStatus(ctx context.Context, code LoginCode) (credentials.Credential, error) {
	if f.PollInterval <= 0 {
		f.PollInterval = 2 * time.Second
	}
	if f.PollTimeout <= 0 {
		f.PollTimeout = 5 * time.Minute
	}
	if f.Sleep == nil {
		f.Sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}

	deadline := time.Now().Add(f.PollTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return credentials.Credential{}, err
		}
		if time.Now().After(deadline) {
			return credentials.Credential{}, errors.New("login polling timed out")
		}

		status, err := f.getLoginStatus(ctx, code)
		if err != nil {
			return credentials.Credential{}, fmt.Errorf("poll login status: %w", err)
		}

		if status.User.ID != "" {
			cred := credentials.Credential{
				ID:              status.User.ID,
				Name:            status.User.Name,
				Email:           status.User.Email,
				AuthToken:       status.User.AuthToken,
				FingerprintID:   code.FingerprintID,
				FingerprintHash: code.FingerprintHash,
			}
			if err := f.Store.Save(ctx, cred); err != nil {
				return credentials.Credential{}, fmt.Errorf("save credential: %w", err)
			}
			return cred, nil
		}

		if status.Error != "" && status.Error != "Authentication failed" {
			return credentials.Credential{}, fmt.Errorf("login status error: %s", status.Error)
		}

		if err := f.Sleep(ctx, f.PollInterval); err != nil {
			return credentials.Credential{}, err
		}
	}
}

// Logout calls the logout endpoint and clears the local credential file.
func (f *Flow) Logout(ctx context.Context) error {
	if f.FingerprintID == nil {
		fid, err := generateFingerprintID()
		if err != nil {
			return fmt.Errorf("generate fingerprint id: %w", err)
		}
		f.FingerprintID = func() (string, error) { return fid, nil }
	}

	fingerprintID, err := f.FingerprintID()
	if err != nil {
		return fmt.Errorf("get fingerprint id: %w", err)
	}

	payload := map[string]any{
		"fingerprintId": fingerprintID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal logout request: %w", err)
	}

	resp, err := f.post(ctx, logoutPath, body)
	if err != nil {
		return fmt.Errorf("logout request: %w", err)
	}
	resp.Body.Close()

	if err := f.Store.Clear(ctx); err != nil {
		return fmt.Errorf("clear credential store: %w", err)
	}

	return nil
}

func (f *Flow) getLoginStatus(ctx context.Context, code LoginCode) (loginStatusResponse, error) {
	url := f.BaseURL + loginStatusPath
	form := fmt.Sprintf("fingerprintId=%s&fingerprintHash=%s", urlEncode(code.FingerprintID), urlEncode(code.FingerprintHash))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return loginStatusResponse{}, fmt.Errorf("create login status request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("X-Requested-With", "XMLHttpRequest")
	httpReq.URL.RawQuery = form

	if f.HTTPClient == nil {
		f.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := f.HTTPClient.Do(httpReq)
	if err != nil {
		return loginStatusResponse{}, fmt.Errorf("send login status request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return loginStatusResponse{}, fmt.Errorf("read login status response: %w", err)
	}

	if len(bodyBytes) == 0 {
		return loginStatusResponse{}, errPendingStatus
	}

	var status loginStatusResponse
	if err := json.Unmarshal(bodyBytes, &status); err != nil {
		return loginStatusResponse{}, fmt.Errorf("decode login status response: %w", err)
	}

	return status, nil
}

func (f *Flow) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	url := f.BaseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Requested-With", "XMLHttpRequest")

	if f.HTTPClient == nil {
		f.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	resp, err := f.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return resp, nil
}

func generateFingerprintID() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func urlEncode(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

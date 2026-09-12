package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearxngSearcher queries a SearXNG instance (self-hosted or public)
// via its JSON API. No API key required.
type SearxngSearcher struct {
	baseURL string
	client  *http.Client
}

// NewSearxngSearcher creates a SearXNG searcher. baseURL is the root of the
// SearXNG instance (e.g. http://127.0.0.1:8888). Empty baseURL disables the
// backend (Search returns empty response without error).
func NewSearxngSearcher(baseURL string) *SearxngSearcher {
	if baseURL == "" {
		return &SearxngSearcher{}
	}
	return &SearxngSearcher{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Enabled reports whether the searcher has a configured instance URL.
func (s *SearxngSearcher) Enabled() bool { return s.baseURL != "" }

// Search performs a web search via SearXNG's /search endpoint.
func (s *SearxngSearcher) Search(ctx context.Context, query string) (Response, error) {
	if !s.Enabled() {
		return Response{Query: query, Backend: "searxng", Results: nil}, nil
	}

	start := time.Now()
	// SearXNG JSON API: /search?q=...&format=json&language=en&safesearch=1
	u := fmt.Sprintf("%s/search?q=%s&format=json&language=en&safesearch=1",
		s.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Response{}, fmt.Errorf("searxng search: %s: %s", resp.Status, body)
	}

	var raw struct {
		Results []struct {
			URL      string  `json:"url"`
			Title    string  `json:"title"`
			Content  string  `json:"content"`
			Engine   string  `json:"engine"`
			Score    float64 `json:"score"`
			Category string  `json:"category"`
		} `json:"results"`
		Query           string `json:"query"`
		NumberOfResults int    `json:"number_of_results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Response{}, err
	}

	results := make([]Result, 0, len(raw.Results))
	for _, r := range raw.Results {
		results = append(results, Result{
			Title:    r.Title,
			URL:      r.URL,
			Snippet:  r.Content,
			Excerpts: []string{r.Content},
		})
	}

	return Response{
		Query:     query,
		Backend:   "searxng",
		Results:   results,
		LatencyMS: time.Since(start).Milliseconds(),
	}, nil
}

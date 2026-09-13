package websearch

import (
	"strings"
	"testing"
)

const htmlFixture = `<!DOCTYPE html>
<html><body>
<div class="result results_links">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fdocs.parallel.ai%2Fsearch%2Fmodes&amp;rut=abc">Parallel <b>Search Modes</b> - Docs</a>
  <a class="result__snippet" href="#">Search offers <strong>four modes</strong> in order of latency &amp; cost.</a>
</div>
<div class="result results_links">
  <a rel="nofollow" class="result__a" href="https://example.com/direct">Direct link result</a>
  <a class="result__snippet" href="#">No wrapper here &lt;b&gt;plain&lt;/b&gt; text.</a>
</div>
</body></html>`

const liteFixture = `<html><body>
<a rel="nofollow" class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Flite.example%2Fone">Lite <em>Result</em> One</a>
<a rel="nofollow" class="result-link" href="https://plain.example/two">Plain Two</a>
</body></html>`

func TestParseResultsHTML(t *testing.T) {
	got := parseResults(htmlFixture, 10)
	if len(got) != 2 {
		t.Fatalf("results = %d, want 2", len(got))
	}
	if got[0].URL != "https://docs.parallel.ai/search/modes" {
		t.Errorf("uddg unwrap failed: %q", got[0].URL)
	}
	if got[0].Title != "Parallel Search Modes - Docs" {
		t.Errorf("title = %q", got[0].Title)
	}
	if !strings.Contains(got[0].Snippet, "four modes") || strings.Contains(got[0].Snippet, "<strong>") {
		t.Errorf("snippet not stripped/unescaped: %q", got[0].Snippet)
	}
	if got[1].URL != "https://example.com/direct" {
		t.Errorf("direct URL mangled: %q", got[1].URL)
	}
}

func TestParseResultsLite(t *testing.T) {
	got := parseResults(liteFixture, 10)
	if len(got) != 2 {
		t.Fatalf("results = %d, want 2", len(got))
	}
	if got[0].URL != "https://lite.example/one" || got[0].Title != "Lite Result One" {
		t.Errorf("lite parse wrong: %+v", got[0])
	}
}

func TestParseResultsMaxCap(t *testing.T) {
	big := strings.Repeat(`<a class="result__a" href="https://e.com/x">T</a>`, 25)
	got := parseResults(big, 10)
	if len(got) != 10 {
		t.Fatalf("results = %d, want capped 10", len(got))
	}
}

func TestUnwrapDDGPlain(t *testing.T) {
	if u := unwrapDDG("https://a.com/b?c=d"); u != "https://a.com/b?c=d" {
		t.Errorf("plain URL changed: %q", u)
	}
}

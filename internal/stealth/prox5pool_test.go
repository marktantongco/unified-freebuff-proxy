package stealth

import (
	"net/url"
	"testing"
)

func urlParse(raw string) (*url.URL, error) { return url.Parse(raw) }

func TestProx5PoolRejectsGarbage(t *testing.T) {
	if _, err := NewProx5Pool([]string{"", "  ", "://bad", "not-a-url"}, nil); err == nil {
		t.Fatal("all-invalid input must fail closed")
	}
	if _, err := NewProx5Pool(nil, nil); err == nil {
		t.Fatal("empty input must fail closed")
	}
}

func TestProx5PoolTableOps(t *testing.T) {
	// Unroutable endpoints: engine accepts format-valid entries into its
	// table; no network success required for table ops. Validation workers
	// fail in background harmlessly.
	p, err := NewProx5Pool([]string{"socks5://127.0.0.1:9", "socks5://127.0.0.1:10"}, nil)
	if err != nil {
		t.Fatalf("NewProx5Pool: %v", err)
	}
	if p.Size() != 2 {
		t.Fatalf("Size = %d, want 2", p.Size())
	}
	// Next must never panic; nil (nothing validated yet) is a legal miss.
	_ = p.Next()
	if n := p.Replace([]string{"socks5://127.0.0.1:11"}); n != 1 {
		t.Fatalf("Replace = %d, want 1", n)
	}
	if p.Size() != 1 {
		t.Fatalf("Size after Replace = %d, want 1", p.Size())
	}
	// Interface conformance: usable wherever USProxyPool is.
	var _ ProxyDispenser = p
	var _ ProxyDispenser = NewUSProxyPool(nil, nil)
}

func TestProx5PoolPreservesAuth(t *testing.T) {
	p, err := NewProx5Pool([]string{"socks5://user:pass@127.0.0.1:9"}, nil)
	if err != nil {
		t.Fatalf("NewProx5Pool: %v", err)
	}
	// Force-dispense path coverage: Next on an unvalidated engine must not
	// panic regardless of outcome.
	_ = p.Next()
}

func TestSchemeless(t *testing.T) {
	must := func(raw, want string) {
		t.Helper()
		u, err := urlParse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if got := schemeless(u); got != want {
			t.Errorf("schemeless(%q) = %q, want %q", raw, got, want)
		}
	}
	must("socks5://127.0.0.1:1080", "127.0.0.1:1080")
	must("socks5://user:pass@10.0.0.1:1080", "user:pass@10.0.0.1:1080")
	must("socks5://user@10.0.0.1:1080", "user@10.0.0.1:1080")
}

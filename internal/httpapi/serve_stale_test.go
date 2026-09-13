package httpapi

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestServeStaleCacheBuildsOnceWithinTTL(t *testing.T) {
	var calls atomic.Int32
	get := ServeStaleCacheWithTTL(func() map[string]any {
		calls.Add(1)
		return map[string]any{"n": calls.Load()}
	}, 500*time.Millisecond)

	a := get()
	b := get()
	if calls.Load() != 1 {
		t.Fatalf("builder called %d times within TTL, want 1", calls.Load())
	}
	if a["n"] != b["n"] {
		t.Fatalf("cached payload differs from fresh build: %v vs %v", a["n"], b["n"])
	}
}

func TestServeStaleCacheServesStaleOnFailedRebuild(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int32
	get := ServeStaleCacheWithTTL(func() map[string]any {
		calls.Add(1)
		if fail.Load() {
			return nil
		}
		return map[string]any{"ok": true}
	}, 50*time.Millisecond)

	if got := get(); got["ok"] != true {
		t.Fatalf("first build failed: %v", got)
	}
	fail.Store(true)
	time.Sleep(80 * time.Millisecond) // expire TTL, force rebuild

	got := get()
	if got["ok"] != true {
		t.Fatalf("stale payload not served after failed rebuild: %v", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("builder calls = %d, want 2 (cache expired, rebuild attempted)", calls.Load())
	}
}

func TestServeStaleCacheNilBuildReturnsNil(t *testing.T) {
	get := ServeStaleCache(nil)
	if get() != nil {
		t.Fatal("nil build should always return nil")
	}
}

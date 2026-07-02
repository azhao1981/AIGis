package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashKey(t *testing.T) {
	// Deterministic: same input -> same digest.
	a := hashKey("secret-key")
	b := hashKey("secret-key")
	if a != b {
		t.Fatalf("hashKey not deterministic: %q != %q", a, b)
	}

	// Known SHA-256 hex of "secret-key".
	const want = "85dbe15d75ef9308c7ae0f33c7a324cc6f4bf519a2ed2f3027bd33c140a4f9aa"
	if a != want {
		t.Fatalf("hashKey(secret-key) = %q, want %q", a, want)
	}

	// Different inputs -> different digests.
	if hashKey("a") == hashKey("b") {
		t.Fatal("distinct inputs produced identical hash")
	}

	// SHA-256 hex digest is always 64 chars.
	if got := hashKey(""); len(got) != 64 {
		t.Fatalf("digest length = %d, want 64", len(got))
	}
}

func TestStartRefreshTicksAndStops(t *testing.T) {
	var ticks atomic.Int64
	p := &PostgresAPIKeyProvider{
		done:      make(chan struct{}),
		refreshFn: func(context.Context) error { ticks.Add(1); return nil },
	}

	p.StartRefresh(20 * time.Millisecond)
	time.Sleep(120 * time.Millisecond)
	p.closeOnce.Do(func() { close(p.done); p.wg.Wait() }) // stop without a real pool

	got := ticks.Load()
	if got < 2 {
		t.Fatalf("refresh ticked %d times in 120ms @20ms, want >= 2", got)
	}

	// After stopping, the count must no longer grow.
	time.Sleep(60 * time.Millisecond)
	if after := ticks.Load(); after != got {
		t.Fatalf("refresh kept ticking after stop: %d -> %d", got, after)
	}
}

func TestStartRefreshDisabled(t *testing.T) {
	var ticks atomic.Int64
	p := &PostgresAPIKeyProvider{
		done:      make(chan struct{}),
		refreshFn: func(context.Context) error { ticks.Add(1); return nil },
	}

	p.StartRefresh(0) // disabled: no goroutine
	time.Sleep(40 * time.Millisecond)
	if got := ticks.Load(); got != 0 {
		t.Fatalf("disabled refresh ticked %d times, want 0", got)
	}
	p.wg.Wait() // must not block (no goroutine was started)
}

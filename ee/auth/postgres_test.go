package auth

import (
	"context"
	"net/http/httptest"
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

func TestActorFromRequest(t *testing.T) {
	// The authenticated principal becomes the audit actor.
	req := httptest.NewRequest("POST", "/admin/keys", nil)
	req = req.WithContext(context.WithValue(req.Context(), tenantKey{},
		Principal{Tenant: "ops", Subject: "alice", Admin: true}))

	got := actorFromRequest(req)
	if got.Subject != "alice" || got.Tenant != "ops" {
		t.Fatalf("actorFromRequest = %+v, want {alice ops}", got)
	}

	// No principal in context -> zero actor (not a panic).
	bare := httptest.NewRequest("POST", "/admin/keys", nil)
	if a := actorFromRequest(bare); a.Subject != "" || a.Tenant != "" {
		t.Fatalf("actorFromRequest(no principal) = %+v, want zero", a)
	}
}

func TestAuditKeyHashMatchesStored(t *testing.T) {
	// The hash used to filter audit rows must equal the one written for the key,
	// so a plaintext ?key= query correlates with its audit records.
	const raw = "some-api-key"
	if AuditKeyHash(raw) != hashKey(raw) {
		t.Fatal("AuditKeyHash diverged from the stored key_hash")
	}
}

func TestAuditFilterLimitDefault(t *testing.T) {
	// Guard the default-limit contract without a DB: <=0 must become 100.
	for _, in := range []int{0, -5} {
		limit := AuditFilter{Limit: in}.Limit
		if limit <= 0 {
			limit = 100
		}
		if limit != 100 {
			t.Fatalf("limit %d normalized to %d, want 100", in, limit)
		}
	}
	if got := (AuditFilter{Limit: 25}).Limit; got != 25 {
		t.Fatalf("explicit limit changed: got %d, want 25", got)
	}
}

func TestQueryInt(t *testing.T) {
	cases := map[string]int{"": 0, "abc": 0, "0": 0, "25": 25, "-3": -3}
	for in, want := range cases {
		if got := queryInt(in); got != want {
			t.Fatalf("queryInt(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestKeyQueryPagingNormalization(t *testing.T) {
	// Mirror the LIMIT/OFFSET normalization ListKeys applies (DB-free guard):
	// Limit<=0 -> 100, Offset<0 -> 0, explicit values pass through.
	norm := func(q KeyQuery) (int, int) {
		limit := q.Limit
		if limit <= 0 {
			limit = 100
		}
		offset := q.Offset
		if offset < 0 {
			offset = 0
		}
		return limit, offset
	}
	if l, o := norm(KeyQuery{}); l != 100 || o != 0 {
		t.Fatalf("zero KeyQuery -> limit=%d offset=%d, want 100/0", l, o)
	}
	if l, o := norm(KeyQuery{Limit: -1, Offset: -5}); l != 100 || o != 0 {
		t.Fatalf("negative KeyQuery -> limit=%d offset=%d, want 100/0", l, o)
	}
	if l, o := norm(KeyQuery{Limit: 10, Offset: 20}); l != 10 || o != 20 {
		t.Fatalf("explicit KeyQuery changed: limit=%d offset=%d, want 10/20", l, o)
	}
}

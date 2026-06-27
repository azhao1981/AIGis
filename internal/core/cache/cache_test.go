package cache

import (
	"testing"
	"time"
)

func TestTTLCacheHitMiss(t *testing.T) {
	c := New(time.Minute, 100)
	if _, ok := c.Get("k"); ok {
		t.Fatal("empty cache should miss")
	}
	c.Set("k", []byte("v"))
	got, ok := c.Get("k")
	if !ok || string(got) != "v" {
		t.Fatalf("Get = %q,%v; want v,true", got, ok)
	}
}

func TestTTLCacheDisabled(t *testing.T) {
	c := New(0, 100) // ttl<=0 → disabled
	c.Set("k", []byte("v"))
	if _, ok := c.Get("k"); ok {
		t.Fatal("disabled cache must always miss")
	}
}

func TestTTLCacheExpiry(t *testing.T) {
	c := New(10*time.Second, 100)
	clk := time.Unix(1000, 0)
	c.now = func() time.Time { return clk }

	c.Set("k", []byte("v"))
	if _, ok := c.Get("k"); !ok {
		t.Fatal("should hit before expiry")
	}
	clk = clk.Add(11 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("should miss after TTL elapses")
	}
}

func TestTTLCacheMaxCap(t *testing.T) {
	c := New(time.Hour, 2) // live entries, never expire within test
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Set("c", []byte("3")) // full of live entries → skipped

	n := 0
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := c.Get(k); ok {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("cache should hold at most max=2 live entries, got %d", n)
	}
}

func TestKeyDeterministicAndDistinct(t *testing.T) {
	k1 := Key("/v1/chat", "body")
	k2 := Key("/v1/chat", "body")
	if k1 != k2 {
		t.Error("Key must be deterministic")
	}
	// separator prevents part-boundary collisions
	if Key("a", "bc") == Key("ab", "c") {
		t.Error("Key parts must not run together")
	}
}

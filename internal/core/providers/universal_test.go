package providers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aigis/internal/core"
	"aigis/internal/core/engine"
	"aigis/internal/core/providers"
)

var placeholderRe = regexp.MustCompile(`__AIGIS_SEC_[0-9a-f]{12}__`)

func TestSend_MaskUnmaskRoundTrip(t *testing.T) {
	var upstreamBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		// Echo the masked placeholder back in an OpenAI-shaped response.
		ph := placeholderRe.FindString(upstreamBody)
		w.Write([]byte(`{"choices":[{"message":{"content":"` + ph + `"}}]}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:         "t",
		Upstream:   engine.Upstream{BaseURL: up.URL, Path: "/v1/chat/completions", AuthStrategy: "none"},
		Transforms: []engine.TransformStep{{Type: "pii", Config: map[string]string{}}},
	}
	p := providers.NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	const secret = "secret@example.com"
	body := []byte(`{"model":"t","messages":[{"role":"user","content":"my email is ` + secret + `"}]}`)
	resp, err := p.Send(ctx, body, http.Header{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Upstream must receive a placeholder, never the plaintext secret.
	if strings.Contains(upstreamBody, secret) {
		t.Errorf("secret leaked to upstream: %s", upstreamBody)
	}
	if !placeholderRe.MatchString(upstreamBody) {
		t.Errorf("upstream got no placeholder; masking did not run: %s", upstreamBody)
	}
	// Response must have the secret restored for the client.
	if !strings.Contains(string(resp), secret) {
		t.Errorf("secret not restored in response: %s", resp)
	}
	if placeholderRe.MatchString(string(resp)) {
		t.Errorf("placeholder leaked to client: %s", resp)
	}
}

func TestSend_HeaderPolicyAndBearerAuth(t *testing.T) {
	t.Setenv("AIGIS_TEST_TOKEN", "tok-123")

	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID: "h",
		Upstream: engine.Upstream{
			BaseURL: up.URL, Path: "/x", AuthStrategy: "bearer", TokenEnv: "AIGIS_TEST_TOKEN",
		},
		HeaderPolicy: engine.HeaderPolicy{
			Allow:  []string{"X-Keep"},
			Set:    map[string]string{"X-Set": "setval"},
			Remove: []string{"X-Remove"},
		},
	}
	p := providers.NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	in := http.Header{}
	in.Set("X-Keep", "keepval")
	in.Set("X-Remove", "rmval")
	in.Set("X-Drop", "dropval") // not in allow-list

	if _, err := p.Send(ctx, []byte(`{}`), in); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got.Get("Authorization") != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", got.Get("Authorization"))
	}
	if got.Get("X-Keep") != "keepval" {
		t.Errorf("allowed header X-Keep = %q, want keepval", got.Get("X-Keep"))
	}
	if got.Get("X-Set") != "setval" {
		t.Errorf("set header X-Set = %q, want setval", got.Get("X-Set"))
	}
	if got.Get("X-Remove") != "" {
		t.Errorf("removed header X-Remove should be absent, got %q", got.Get("X-Remove"))
	}
	if got.Get("X-Drop") != "" {
		t.Errorf("non-allowed header X-Drop should be dropped, got %q", got.Get("X-Drop"))
	}
}

// TestSend_ForceBlockEgressLeak verifies the strict-review safety net: on a
// force_block route, if a secret survives masking (here the PII transform is
// scoped to Email only, so an OpenAI key passes through), Send must refuse to
// egress — returning an error and never calling the upstream.
func TestSend_ForceBlockEgressLeak(t *testing.T) {
	called := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:         "fb",
		ForceBlock: true,
		Upstream:   engine.Upstream{BaseURL: up.URL, Path: "/x", AuthStrategy: "none"},
		// Scope masking to Email only, so the OpenAI key survives masking.
		Transforms: []engine.TransformStep{{Type: "pii", Config: map[string]string{"rules": "Email"}}},
	}
	p := providers.NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	body := []byte(`{"model":"fb","messages":[{"role":"user","content":"key sk-abcdefghijklmnopqrstuvwx"}]}`)
	_, err := p.Send(ctx, body, http.Header{})
	if err == nil {
		t.Fatal("expected egress block error, got nil")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Errorf("error should mention egress block, got %v", err)
	}
	if called {
		t.Error("upstream must NOT be called when egress is blocked")
	}
}

// TestSend_ForceBlockCleanPasses verifies force_block does not interfere when
// masking is complete: a fully-masked body egresses normally.
func TestSend_ForceBlockCleanPasses(t *testing.T) {
	called := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:         "fbc",
		ForceBlock: true,
		Upstream:   engine.Upstream{BaseURL: up.URL, Path: "/x", AuthStrategy: "none"},
		Transforms: []engine.TransformStep{{Type: "pii", Config: map[string]string{}}},
	}
	p := providers.NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	// The key IS masked (default rules include OpenAI key), so nothing survives.
	body := []byte(`{"model":"fbc","messages":[{"role":"user","content":"key sk-abcdefghijklmnopqrstuvwx"}]}`)
	if _, err := p.Send(ctx, body, http.Header{}); err != nil {
		t.Fatalf("clean force_block request should pass, got %v", err)
	}
	if !called {
		t.Error("upstream should be called when body is fully masked")
	}
}

func TestSend_UpstreamErrorSurfaces(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:       "e",
		Upstream: engine.Upstream{BaseURL: up.URL, Path: "/x", AuthStrategy: "none"},
	}
	p := providers.NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	_, err := p.Send(ctx, []byte(`{"model":"e"}`), http.Header{})
	if err == nil {
		t.Fatal("expected error for non-200 upstream, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should carry upstream message, got %v", err)
	}
}

// TestSend_RetryOn5xxThenSucceeds verifies the retry policy re-tries a transient
// 5xx and succeeds on a later attempt.
func TestSend_RetryOn5xxThenSucceeds(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"try later"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:       "r5",
		Upstream: engine.Upstream{BaseURL: up.URL, Path: "/x", AuthStrategy: "none"},
	}
	p := providers.NewUniversalProvider(route, nil, nil).
		WithRetry(providers.RetryPolicy{MaxAttempts: 3, Backoff: time.Millisecond})
	ctx := core.NewGatewayContext(context.Background(), nil)

	if _, err := p.Send(ctx, []byte(`{"model":"r5"}`), http.Header{}); err != nil {
		t.Fatalf("Send should succeed after retries, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("upstream hits = %d, want 3", got)
	}
}

// TestSend_RetryExhausted verifies retry stops after MaxAttempts on persistent 5xx.
func TestSend_RetryExhausted(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":{"message":"down"}}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:       "rex",
		Upstream: engine.Upstream{BaseURL: up.URL, Path: "/x", AuthStrategy: "none"},
	}
	p := providers.NewUniversalProvider(route, nil, nil).
		WithRetry(providers.RetryPolicy{MaxAttempts: 2, Backoff: time.Millisecond})
	ctx := core.NewGatewayContext(context.Background(), nil)

	if _, err := p.Send(ctx, []byte(`{"model":"rex"}`), http.Header{}); err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("upstream hits = %d, want 2 (MaxAttempts)", got)
	}
}

// TestSend_No4xxRetry verifies a 4xx client error is returned immediately (one hit).
func TestSend_No4xxRetry(t *testing.T) {
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad"}}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID:       "r4",
		Upstream: engine.Upstream{BaseURL: up.URL, Path: "/x", AuthStrategy: "none"},
	}
	p := providers.NewUniversalProvider(route, nil, nil).
		WithRetry(providers.RetryPolicy{MaxAttempts: 3, Backoff: time.Millisecond})
	ctx := core.NewGatewayContext(context.Background(), nil)

	if _, err := p.Send(ctx, []byte(`{"model":"r4"}`), http.Header{}); err == nil {
		t.Fatal("expected 4xx error, got nil")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (no retry on 4xx)", got)
	}
}

// TestSend_MultiKeyRoundRobin verifies TokenEnvs round-robins the bearer token
// across configured keys, and skips an env that resolves to empty.
func TestSend_MultiKeyRoundRobin(t *testing.T) {
	t.Setenv("AIGIS_K1", "key-a")
	t.Setenv("AIGIS_K2", "key-b")
	t.Setenv("AIGIS_K3", "") // empty: must be skipped

	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID: "mk",
		Upstream: engine.Upstream{
			BaseURL: up.URL, Path: "/x", AuthStrategy: "bearer",
			TokenEnvs: []string{"AIGIS_K1", "AIGIS_K3", "AIGIS_K2"},
		},
	}
	p := providers.NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	for i := 0; i < 4; i++ {
		if _, err := p.Send(ctx, []byte(`{}`), http.Header{}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	// Round-robin over the two non-empty keys: a, b, a, b.
	want := []string{"key-a", "key-b", "key-a", "key-b"}
	for i, w := range want {
		if seen[i] != w {
			t.Errorf("request %d token = %q, want %q (all: %v)", i, seen[i], w, seen)
		}
	}
}

// TestSend_MultiKeyFallbackToSingle verifies that when TokenEnvs is empty, the
// single TokenEnv is used.
func TestSend_MultiKeyFallbackToSingle(t *testing.T) {
	t.Setenv("AIGIS_SINGLE", "solo-key")

	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{}`))
	}))
	defer up.Close()

	route := &engine.Route{
		ID: "sk",
		Upstream: engine.Upstream{
			BaseURL: up.URL, Path: "/x", AuthStrategy: "bearer", TokenEnv: "AIGIS_SINGLE",
		},
	}
	p := providers.NewUniversalProvider(route, nil, nil)
	ctx := core.NewGatewayContext(context.Background(), nil)

	if _, err := p.Send(ctx, []byte(`{}`), http.Header{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got != "Bearer solo-key" {
		t.Errorf("Authorization = %q, want Bearer solo-key", got)
	}
}

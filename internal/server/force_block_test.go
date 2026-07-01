package server_test

import (
	"bufio"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/viper"

	"aigis/internal/pkg/logger"
	"aigis/internal/server"
)

// readAllSSE drains an SSE response body into a single string.
func readAllSSE(t *testing.T, body *bufio.Scanner) string {
	t.Helper()
	out := new(bytes.Buffer)
	for body.Scan() {
		out.WriteString(body.Text())
		out.WriteString("\n")
	}
	return out.String()
}

// TestForceBlockPseudoStream verifies the strict-review downgrade: a stream:true
// request on a force_block route is served internally via the blocking path,
// then re-emitted to the client as a pseudo-stream SSE (one data event + [DONE]).
// A clean (fully-masked) body must pass through and reach the upstream.
func TestForceBlockPseudoStream(t *testing.T) {
	var called atomic.Bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body)
		// The email must have been masked before egress.
		if strings.Contains(body.String(), "secret@example.com") {
			t.Errorf("secret leaked to upstream: %s", body.String())
		}
		// Blocking upstream returns a normal (non-SSE) JSON body.
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer mock.Close()

	viper.Set("engine.routes", []map[string]any{
		{
			"id":          "fb-stream",
			"matcher":     map[string]string{"model": "^fb-.*"},
			"force_block": true,
			"upstream": map[string]any{
				"base_url":      mock.URL,
				"path":          "/v1/chat/completions",
				"auth_strategy": "none",
			},
			"transforms": []map[string]any{
				{"type": "pii", "config": map[string]string{}},
			},
		},
		{
			"id":      "fallback",
			"matcher": map[string]any{},
			"upstream": map[string]any{
				"base_url":      mock.URL,
				"path":          "/v1/chat/completions",
				"auth_strategy": "none",
			},
		},
	})
	defer viper.Set("engine.routes", nil)

	zapLog, _ := logger.New("error")
	srv, err := server.NewHTTPServer(":0", zapLog)
	if err != nil {
		t.Fatalf("NewHTTPServer failed: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqBody := `{"model":"fb-1","stream":true,"messages":[{"role":"user","content":"mail secret@example.com"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// The client still sees an SSE stream, unaware of the internal downgrade.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected SSE content-type, got %q", ct)
	}

	got := readAllSSE(t, bufio.NewScanner(resp.Body))

	if !called.Load() {
		t.Error("upstream should be called for a clean force_block request")
	}
	if !strings.Contains(got, "data: ") {
		t.Errorf("expected SSE data event, got:\n%s", got)
	}
	if !strings.Contains(got, "[DONE]") {
		t.Errorf("expected [DONE] terminator, got:\n%s", got)
	}
	if !strings.Contains(got, "\"content\":\"ok\"") {
		t.Errorf("expected buffered result re-emitted in SSE, got:\n%s", got)
	}
}

// TestForceBlockEgressBlockedStream verifies the safety net over the pseudo-stream
// path: when masking is scoped so a secret survives (Email-only, but body carries
// an OpenAI key), the egress check blocks the request — the upstream is never
// called and the client gets an SSE error event instead of data.
func TestForceBlockEgressBlockedStream(t *testing.T) {
	var called atomic.Bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	viper.Set("engine.routes", []map[string]any{
		{
			"id":          "fb-leak",
			"matcher":     map[string]string{"model": "^fb-.*"},
			"force_block": true,
			"upstream": map[string]any{
				"base_url":      mock.URL,
				"path":          "/v1/chat/completions",
				"auth_strategy": "none",
			},
			// Scope masking to Email only, so an OpenAI key survives masking.
			"transforms": []map[string]any{
				{"type": "pii", "config": map[string]string{"rules": "Email"}},
			},
		},
		{
			"id":      "fallback",
			"matcher": map[string]any{},
			"upstream": map[string]any{
				"base_url":      mock.URL,
				"path":          "/v1/chat/completions",
				"auth_strategy": "none",
			},
		},
	})
	defer viper.Set("engine.routes", nil)

	zapLog, _ := logger.New("error")
	srv, err := server.NewHTTPServer(":0", zapLog)
	if err != nil {
		t.Fatalf("NewHTTPServer failed: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqBody := `{"model":"fb-1","stream":true,"messages":[{"role":"user","content":"key sk-abcdefghijklmnopqrstuvwx"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	got := readAllSSE(t, bufio.NewScanner(resp.Body))

	if called.Load() {
		t.Error("upstream must NOT be called when egress is blocked")
	}
	if !strings.Contains(got, "error") {
		t.Errorf("expected SSE error event on egress block, got:\n%s", got)
	}
	// The surviving key must never appear in what the client receives.
	if strings.Contains(got, "sk-abcdefghijklmnopqrstuvwx") {
		t.Errorf("blocked secret leaked to client:\n%s", got)
	}
}

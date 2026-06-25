package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"aigis/internal/pkg/logger"
	"aigis/internal/server"
)

// newGatewayServer spins up an AIGis HTTP server whose single route points at
// the given upstream URL with PII masking enabled. Returns the test server.
func newGatewayServer(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	viper.Set("engine.routes", []map[string]any{
		{
			"id":      "test",
			"matcher": map[string]string{"model": "^test-.*"},
			"upstream": map[string]any{
				"base_url":      upstreamURL,
				"path":          "/v1/chat/completions",
				"auth_strategy": "none",
			},
			"transforms": []map[string]any{
				{"type": "pii", "config": map[string]string{}},
			},
		},
		// Catch-all so the config passes engine validation (mirrors production
		// convention). Tests only send test-* models, so it is never matched.
		{
			"id":      "fallback",
			"matcher": map[string]any{},
			"upstream": map[string]any{
				"base_url":      upstreamURL,
				"path":          "/v1/chat/completions",
				"auth_strategy": "none",
			},
		},
	})
	t.Cleanup(func() { viper.Set("engine.routes", nil) })

	zapLog, _ := logger.New("error")
	srv, err := server.NewHTTPServer(":0", zapLog)
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestHandleGateway_BlockingRoundTrip(t *testing.T) {
	var upstreamBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		ph := placeholderRe.FindString(upstreamBody)
		w.Write([]byte(`{"choices":[{"message":{"content":"` + ph + `"}}]}`))
	}))
	defer up.Close()

	ts := newGatewayServer(t, up.URL)

	const secret = "secret@example.com"
	reqBody := `{"model":"test-1","messages":[{"role":"user","content":"email ` + secret + `"}]}`
	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if strings.Contains(upstreamBody, secret) {
		t.Errorf("secret leaked to upstream: %s", upstreamBody)
	}
	if !strings.Contains(string(got), secret) {
		t.Errorf("secret not restored in response: %s", got)
	}
	if placeholderRe.MatchString(string(got)) {
		t.Errorf("placeholder leaked to client: %s", got)
	}
}

func TestHandleGateway_MethodNotAllowed(t *testing.T) {
	ts := newGatewayServer(t, "http://unused.invalid")

	resp, err := http.Get(ts.URL + "/v1/messages")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()

	ts := newGatewayServer(t, up.URL)

	// Drive one successful request through the gateway.
	reqBody := `{"model":"test-1","messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	// /metrics must reflect it.
	mResp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer mResp.Body.Close()

	var snap struct {
		InFlight int64 `json:"in_flight"`
		Total    int64 `json:"total_requests"`
		Success  int64 `json:"success"`
		Failed   int64 `json:"failed"`
	}
	if err := json.NewDecoder(mResp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if snap.Total != 1 || snap.Success != 1 {
		t.Errorf("metrics = %+v, want total=1 success=1", snap)
	}
	if snap.InFlight != 0 {
		t.Errorf("in_flight = %d, want 0 after request done", snap.InFlight)
	}
}

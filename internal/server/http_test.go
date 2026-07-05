package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestHealthEndpoint(t *testing.T) {
	ts := newGatewayServer(t, "http://unused.invalid")

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var h struct {
		Status string            `json:"status"`
		Routes map[string]string `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	// Breaker disabled by default: every route reports "closed" → status "ok".
	if h.Status != "ok" {
		t.Errorf("status = %q, want ok", h.Status)
	}
	if h.Routes["test"] != "closed" || h.Routes["fallback"] != "closed" {
		t.Errorf("routes = %v, want all closed", h.Routes)
	}
}

func TestRoutesInfoEndpoint(t *testing.T) {
	// A token value that must NEVER appear in the response — only its env NAME may.
	t.Setenv("AIGIS_ROUTESINFO_SECRET", "super-secret-token-value")

	viper.Set("engine.routes", []map[string]any{
		{
			"id":      "info",
			"matcher": map[string]string{"model": "^info-.*"},
			"upstream": map[string]any{
				"base_url":      "http://up.invalid",
				"path":          "/x",
				"auth_strategy": "bearer",
				"token_env":     "AIGIS_ROUTESINFO_SECRET",
			},
			"transforms": []map[string]any{{"type": "pii", "config": map[string]string{}}},
		},
		{
			"id":       "fallback",
			"matcher":  map[string]any{},
			"upstream": map[string]any{"base_url": "http://up.invalid", "path": "/x", "auth_strategy": "none"},
		},
	})
	t.Cleanup(func() { viper.Set("engine.routes", nil) })
	viper.Set("retry.max_attempts", 3)
	viper.Set("retry.backoff_ms", 250)
	t.Cleanup(func() { viper.Set("retry.max_attempts", nil); viper.Set("retry.backoff_ms", nil) })

	zapLog, _ := logger.New("error")
	srv, err := server.NewHTTPServer(":0", zapLog)
	if err != nil {
		t.Fatalf("NewHTTPServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/admin/routes-info")
	if err != nil {
		t.Fatalf("GET /admin/routes-info: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)

	// The token VALUE must never leak; only the env NAME is allowed.
	if strings.Contains(string(raw), "super-secret-token-value") {
		t.Fatalf("token value leaked in routes-info: %s", raw)
	}

	var body struct {
		Retry struct {
			MaxAttempts int   `json:"max_attempts"`
			BackoffMs   int64 `json:"backoff_ms"`
		} `json:"retry"`
		Routes []struct {
			ID       string `json:"id"`
			Upstream struct {
				AuthStrategy  string `json:"auth_strategy"`
				TokenEnv      string `json:"token_env"`
				TokenKeyCount int    `json:"token_key_count"`
			} `json:"upstream"`
			Transforms   []string `json:"transforms"`
			BreakerState string   `json:"breaker_state"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode routes-info: %v", err)
	}

	if body.Retry.MaxAttempts != 3 || body.Retry.BackoffMs != 250 {
		t.Errorf("retry = %+v, want max_attempts=3 backoff_ms=250", body.Retry)
	}
	if len(body.Routes) != 2 {
		t.Fatalf("routes count = %d, want 2", len(body.Routes))
	}
	var info *struct {
		ID       string `json:"id"`
		Upstream struct {
			AuthStrategy  string `json:"auth_strategy"`
			TokenEnv      string `json:"token_env"`
			TokenKeyCount int    `json:"token_key_count"`
		} `json:"upstream"`
		Transforms   []string `json:"transforms"`
		BreakerState string   `json:"breaker_state"`
	}
	for i := range body.Routes {
		if body.Routes[i].ID == "info" {
			info = &body.Routes[i]
		}
	}
	if info == nil {
		t.Fatal("route 'info' missing from routes-info")
	}
	if info.Upstream.TokenEnv != "AIGIS_ROUTESINFO_SECRET" {
		t.Errorf("token_env = %q, want AIGIS_ROUTESINFO_SECRET (name only)", info.Upstream.TokenEnv)
	}
	if info.Upstream.TokenKeyCount != 1 {
		t.Errorf("token_key_count = %d, want 1", info.Upstream.TokenKeyCount)
	}
	if len(info.Transforms) != 1 || info.Transforms[0] != "pii" {
		t.Errorf("transforms = %v, want [pii]", info.Transforms)
	}
	if info.BreakerState != "closed" {
		t.Errorf("breaker_state = %q, want closed", info.BreakerState)
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

// TestAuditEndpoint drives a PII-masking request through the gateway, then
// verifies GET /admin/audit exposes the resulting record — metadata only,
// never the plaintext secret — and that rule/limit filters work.
func TestAuditEndpoint(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()

	ts := newGatewayServer(t, up.URL)

	const secret = "audit-probe@example.com"
	reqID := "audit-test-" + strings.ReplaceAll(t.Name(), "/", "-")
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/messages",
		strings.NewReader(`{"model":"test-1","messages":[{"role":"user","content":"mail `+secret+`"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", reqID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	// The audit line is written by a defer as the gateway handler returns, which
	// can race the client seeing the response; poll briefly.
	type auditResp struct {
		Enabled bool `json:"enabled"`
		Records []struct {
			RequestID string         `json:"request_id"`
			Total     int            `json:"total"`
			ByType    map[string]int `json:"by_type"`
		} `json:"records"`
	}
	var mine *struct {
		RequestID string         `json:"request_id"`
		Total     int            `json:"total"`
		ByType    map[string]int `json:"by_type"`
	}
	var raw []byte
	for i := 0; i < 50 && mine == nil; i++ {
		aResp, err := http.Get(ts.URL + "/admin/audit?rule=Email&limit=10")
		if err != nil {
			t.Fatalf("GET /admin/audit: %v", err)
		}
		raw, _ = io.ReadAll(aResp.Body)
		aResp.Body.Close()
		if aResp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", aResp.StatusCode, raw)
		}
		var body auditResp
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode audit: %v (body %s)", err, raw)
		}
		if !body.Enabled {
			t.Fatal("enabled = false, want true")
		}
		for i := range body.Records {
			if body.Records[i].RequestID == reqID {
				mine = &body.Records[i]
				break
			}
		}
		if mine == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if mine == nil {
		t.Fatalf("audit record for %s not found (last body %s)", reqID, raw)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("plaintext secret leaked via /admin/audit: %s", raw)
	}
	if mine.ByType["Email"] < 1 {
		t.Errorf("by_type = %v, want Email >= 1", mine.ByType)
	}

	// Rule filter: a rule that never fired must return no records.
	fResp, err := http.Get(ts.URL + "/admin/audit?rule=NoSuchRule")
	if err != nil {
		t.Fatalf("GET filtered: %v", err)
	}
	fRaw, _ := io.ReadAll(fResp.Body)
	fResp.Body.Close()
	var filtered auditResp
	if err := json.Unmarshal(fRaw, &filtered); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if len(filtered.Records) != 0 {
		t.Errorf("filtered records = %d, want 0", len(filtered.Records))
	}

	// Non-GET is rejected.
	pResp, err := http.Post(ts.URL+"/admin/audit", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /admin/audit: %v", err)
	}
	pResp.Body.Close()
	if pResp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", pResp.StatusCode)
	}
}

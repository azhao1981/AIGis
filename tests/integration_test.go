package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"aigis/internal/config"
	"aigis/internal/pkg/logger"
	"aigis/internal/server"
)

var placeholderRe = regexp.MustCompile(`__AIGIS_SEC_[0-9a-f]{12}__`)

// defaultTestRoutes is a minimal config that passes engine validation (a single
// catch-all route). Tests that don't care about routing rely on it; tests that
// set their own routes restore it on cleanup.
func defaultTestRoutes() []map[string]any {
	return []map[string]any{
		{
			"id":      "fallback",
			"matcher": map[string]any{},
			"upstream": map[string]any{
				"base_url":      "http://unused.invalid",
				"path":          "/chat/completions",
				"auth_strategy": "none",
			},
		},
	}
}

func TestMain(m *testing.M) {
	config.Init("")
	// Provide a valid default engine config so NewHTTPServer (which now validates
	// config at startup) succeeds for tests that don't configure their own routes.
	viper.Set("engine.routes", defaultTestRoutes())
	os.Exit(m.Run())
}

func newTestServer() *httptest.Server {
	log, _ := logger.New("error")
	srv, err := server.NewHTTPServer(":0", log)
	if err != nil {
		panic(err)
	}
	return httptest.NewServer(srv.Handler())
}

func TestHealthEndpoint(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("期望状态 200，得到 %d", resp.StatusCode)
	}
}

func TestRootEndpoint(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)

	if result["message"] != "AIGis is running" {
		t.Errorf("期望 'AIGis is running'，得到 '%s'", result["message"])
	}
}

func TestChatCompletionsMethodNotAllowed(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("期望状态 405，得到 %d", resp.StatusCode)
	}
}

// TestChatCompletionsPIIRoundTrip verifies the non-streaming PII pipeline
// end-to-end against a mock upstream (no real network / cwd dependency):
// request PII is masked before reaching upstream, and the response is unmasked
// back to the client.
func TestChatCompletionsPIIRoundTrip(t *testing.T) {
	// Mock upstream echoes the (already-masked) content back in OpenAI format.
	var gotUpstreamBody string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotUpstreamBody = string(b)
		placeholder := placeholderRe.FindString(gotUpstreamBody)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "你给的是 " + placeholder}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	viper.Set("engine.routes", []map[string]any{
		{
			"id":      "test-openai",
			"matcher": map[string]string{"model": "^gpt-.*"},
			"upstream": map[string]any{
				"base_url":      mock.URL,
				"path":          "/chat/completions",
				"auth_strategy": "none",
			},
			"transforms": []map[string]any{{"type": "pii", "config": map[string]string{}}},
		},
		// Catch-all so the config passes engine validation; gpt-* hits the route above.
		{
			"id":      "fallback",
			"matcher": map[string]any{},
			"upstream": map[string]any{
				"base_url":      mock.URL,
				"path":          "/chat/completions",
				"auth_strategy": "none",
			},
		},
	})
	defer viper.Set("engine.routes", defaultTestRoutes())

	ts := newTestServer()
	defer ts.Close()

	const email = "dangerous@coder.com"
	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{"role": "user", "content": "My email is " + email + " and my phone is 13800138000"},
		},
	})

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		t.Fatalf("期望状态 200，得到 %d: %s", resp.StatusCode, buf.String())
	}

	// 1. Upstream must have received masked content (no raw PII).
	if strings.Contains(gotUpstreamBody, email) {
		t.Errorf("上游收到了未脱敏的邮箱: %s", gotUpstreamBody)
	}
	if !placeholderRe.MatchString(gotUpstreamBody) {
		t.Errorf("上游请求中未见占位符（PII 未脱敏）: %s", gotUpstreamBody)
	}

	// 2. Response to client must be unmasked back to the original email.
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	content := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if !strings.Contains(content, email) {
		t.Errorf("响应未还原原始邮箱: %s", content)
	}
	if placeholderRe.MatchString(content) {
		t.Errorf("响应中占位符未还原: %s", content)
	}
}

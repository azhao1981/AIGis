package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/spf13/viper"

	"aigis/internal/config"
	"aigis/internal/pkg/logger"
	"aigis/internal/server"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestMain(m *testing.M) {
	config.Init("")
	os.Exit(m.Run())
}

func newTestServer() *httptest.Server {
	log, _ := logger.New("info")
	srv := server.NewHTTPServer(":0", log)
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

func TestChatCompletions(t *testing.T) {
	apiKey := viper.GetString("openai.api_key")
	if apiKey == "" {
		// Debug: 打印所有 openai 相关配置
		t.Logf("openai.api_key: '%s'", viper.GetString("openai.api_key"))
		t.Logf("openai.base_url: '%s'", viper.GetString("openai.base_url"))
		t.Skip("跳过: 未设置 OPENAI_API_KEY")
	}
	t.Logf("Using API key: %s...", apiKey[:min(10, len(apiKey))])

	ts := newTestServer()
	defer ts.Close()

	// Test message with PII
	testContent := "检查话里有没有敏感信息，没有或已经脱敏就原话返回： My email is dangerous@coder.com and my phone is 13800138000"
	t.Logf("Testing PII redaction with content: %s", testContent)

	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{"role": "user", "content": testContent},
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

	// Read and log the response
	var response map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&response)
	if err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	// Log the full response for debugging
	t.Logf("Response: %+v", response)

	// Check if the response has choices
	if choices, ok := response["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if content, ok := message["content"].(string); ok {
					t.Logf("Response content: %s", content)
				}
			}
		}
	}
}

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
	srv := server.NewHTTPServer(":0")
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

	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{"role": "user", "content": "Say hi"},
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
}

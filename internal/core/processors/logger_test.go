package processors

import (
	"testing"

	"aigis/internal/core"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
)

func TestRequestLoggerWithCallerInfo(t *testing.T) {
	// 创建一个 observer 来捕获日志
	observedLogs := observer.New(zap.InfoLevel)
	testLogger := zap.New(observedLogs, zap.AddCaller(), zap.AddCallerSkip(1))

	// 创建 RequestLogger
	requestLogger := NewRequestLogger()

	// 创建一个 AIGisContext
	ctx := core.NewGatewayContext(testLogger, testLogger)

	// 创建测试请求体
	body := []byte(`{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}`)

	// 调用 OnRequest
	_, err := requestLogger.OnRequest(ctx, body)
	if err != nil {
		t.Fatalf("OnRequest failed: %v", err)
	}

	// 检查日志
	logs := observedLogs.All()
	if len(logs) != 1 {
		t.Fatalf("Expected 1 log entry, got %d", len(logs))
	}

	log := logs[0]

	// 验证消息
	if log.Message != "Request Started" {
		t.Errorf("Expected message 'Request Started', got '%s'", log.Message)
	}

	// 验证 caller 信息
	caller := log.Caller
	if caller == nil {
		t.Fatal("Expected caller information, got nil")
	}

	// caller 应该显示这个测试文件，而不是 processors/logger.go
	if !contains(caller.File, "logger_test.go") {
		t.Errorf("Expected caller file to contain 'logger_test.go', got %s", caller.File)
	}

	t.Logf("Caller: %s:%d", caller.File, caller.Line)

	// 检查日志字段
	expectedFields := map[string]interface{}{
		"method": "POST",
		"path":   "/v1/chat/completions",
		"model":  "gpt-4",
	}

	for key, expected := range expectedFields {
		fieldValue, found := log.ContextMap()[key]
		if !found {
			t.Errorf("Expected field '%s' not found in log", key)
			continue
		}
		if fieldValue != expected {
			t.Errorf("Expected field '%s' to be '%v', got '%v'", key, expected, fieldValue)
		}
	}
}

// contains 检查字符串是否包含子字符串
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) &&
			(s[:len(substr)] == substr ||
			 s[len(s)-len(substr):] == substr ||
			 indexOf(s, substr) >= 0)))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestRequestLoggerOnResponse(t *testing.T) {
	// 创建测试 logger
	observedLogs := observer.New(zap.InfoLevel)
	testLogger := zap.New(observedLogs, zap.AddCaller(), zap.AddCallerSkip(1))

	// 创建 RequestLogger
	requestLogger := NewRequestLogger()

	// 创建一个 AIGisContext
	ctx := core.NewGatewayContext(testLogger, testLogger)

	// 创建测试响应体
	body := []byte(`{"choices": [{"message": {"content": "test response"}}]}`)

	// 调用 OnResponse
	_, err := requestLogger.OnResponse(ctx, body)
	if err != nil {
		t.Fatalf("OnResponse failed: %v", err)
	}

	// 检查日志
	logs := observedLogs.All()
	if len(logs) != 1 {
		t.Fatalf("Expected 1 log entry, got %d", len(logs))
	}

	log := logs[0]

	// 验证消息
	if log.Message != "Request Finished" {
		t.Errorf("Expected message 'Request Finished', got '%s'", log.Message)
	}

	// 检查 latency 字段存在
	latencyField, found := log.ContextMap()["latency"]
	if !found {
		t.Error("Expected 'latency' field not found in log")
	}
	if latencyField == nil {
		t.Error("Latency field should not be nil")
	}

	// 检查 status 字段
	statusField, found := log.ContextMap()["status"]
	if !found {
		t.Error("Expected 'status' field not found in log")
	}
	if statusField != "Success" {
		t.Errorf("Expected status 'Success', got '%v'", statusField)
	}
}
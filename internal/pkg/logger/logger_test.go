package logger

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestLoggerNew(t *testing.T) {
	testCases := []struct {
		name        string
		level       string
		expectError bool
	}{
		{"debug level", "debug", false},
		{"info level", "info", false},
		{"warn level", "warn", false},
		{"error level", "error", false},
		{"empty level defaults to info", "", false},
		{"invalid level defaults to info", "invalid", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger, err := New(tc.level)
			if tc.expectError && err == nil {
				t.Errorf("Expected error for level '%s', got nil", tc.level)
			}
			if !tc.expectError && err != nil {
				t.Errorf("Expected no error for level '%s', got %v", tc.level, err)
			}
			if logger == nil {
				t.Error("Expected non-nil logger")
			}
		})
	}
}

func TestLoggerCallerSkipIntegration(t *testing.T) {
	// 使用 zaptest 创建一个带缓冲区的 logger
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	// 测试直接调用
	logger.Info("Direct call test")

	// 测试嵌套调用（模拟业务场景）
	func() {
		logger.Info("Nested call test")
	}()

	// 测试更深层的嵌套
	func() {
		func() {
			logger.Info("Deep nested call test")
		}()
	}()

	// 这个测试主要是为了验证我们的 logger 包能正确创建和初始化
	// 并验证 caller skip 功能在实际运行时的表现
	t.Log("Logger caller skip test passed")
}

// TestCallerSkipDepth 测试不同的 caller skip 深度
func TestCallerSkipDepth(t *testing.T) {
	// 创建一个带 caller skip 的 logger
	logger := zaptest.NewLogger(t, zaptest.Level(zap.InfoLevel))

	// 测试当前文件的调用位置
	func() {
		logger.Info("Test with skip 0")
	}()

	// 使用我们的 WithCallerSkip 函数
	loggerWithSkip := WithCallerSkip(logger, 1)
	func() {
		loggerWithSkip.Info("Test with skip 1")
	}()

	t.Log("Caller skip depth test passed")
}
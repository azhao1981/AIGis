package processors

import (
	"encoding/json"
	"time"

	"aigis/internal/core"
	"go.uber.org/zap"
)

// RequestLogger 是一个记录请求日志的处理器
type RequestLogger struct {
	name     string
	priority int
}

// NewRequestLogger 创建一个新的请求日志处理器
func NewRequestLogger() *RequestLogger {
	return &RequestLogger{
		name:     "request-logger",
		priority: -100, // 必须是第一个执行
	}
}

// Name 返回处理器名称
func (r *RequestLogger) Name() string {
	return r.name
}

// Priority 返回处理器优先级
func (r *RequestLogger) Priority() int {
	return r.priority
}

// OnRequest 处理请求
func (r *RequestLogger) OnRequest(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	// 解析请求体以获取模型信息
	var request struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &request)

	// 记录请求开始 - 不需要重复添加 request_id 和 trace_id
	// 因为它们已经在创建 reqLogger 时通过 With() 方法注入了
	ctx.Log.Info("Request Started",
		zap.String("method", "POST"),
		zap.String("path", "/v1/chat/completions"),
		zap.String("model", request.Model),
	)

	// 直接返回原始 body，不做修改
	return body, nil
}

// OnResponse 处理响应
func (r *RequestLogger) OnResponse(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	// 计算延迟
	latency := time.Since(ctx.StartTime)

	// 记录请求完成 - 不需要重复添加 request_id 和 trace_id
	// zap.Duration() 会自动格式化为带有单位的字符串
	ctx.Log.Info("Request Finished",
		zap.Duration("latency", latency),
		zap.String("status", "Success"),
	)

	// 直接返回原始 body，不做修改
	return body, nil
}
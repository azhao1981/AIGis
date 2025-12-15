package processors

import (
	"time"

	"aigis/internal/core"

	"github.com/bytedance/sonic"
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
	// 从 JSON 中直接提取 model 字段
	model, _ := sonic.Get(body, "model")
	modelStr, _ := model.String()

	// 记录请求开始 - logger 会自动获取调用者信息
	ctx.Log.Info("Request Started",
		zap.String("method", "POST"),
		zap.String("path", "/v1/chat/completions"),
		zap.String("model", modelStr),
	)

	// 直接返回原始 body，不做修改
	return body, nil
}

// OnResponse 处理响应
func (r *RequestLogger) OnResponse(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	// 计算延迟
	latency := time.Since(ctx.StartTime)

	// 将延迟转换为毫秒，保留三位小数
	// TODO：这里为什么在这里定义？这是瞎写的吗？
	latencyMs := float64(latency.Nanoseconds()) / 1000000
	// 四舍五入到三位小数
	latencyMs = float64(int64(latencyMs*1000+0.5)) / 1000

	// 记录请求完成 - logger 会自动获取调用者信息
	ctx.Log.Info("Request Finished",
		zap.Float64("latency_ms", latencyMs),
		zap.String("status", "Success"),
	)

	// 直接返回原始 body，不做修改
	return body, nil
}

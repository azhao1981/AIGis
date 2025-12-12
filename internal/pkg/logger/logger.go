package logger

import (
	"fmt"
	"runtime"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New 创建一个新的 zap logger实例
// level: 日志级别 (debug, info, warn, error)
// 返回配置好的 logger 和可能的错误
func New(level string) (*zap.Logger, error) {
	return NewWithCallerSkip(level, 0)
}

// NewWithCallerSkip 创建一个新的 zap logger实例，并设置 caller skip
// level: 日志级别 (debug, info, warn, error)
// skip: 跳过的调用栈层数
// 返回配置好的 logger 和可能的错误
func NewWithCallerSkip(level string, skip int) (*zap.Logger, error) {
	// 使用生产配置（JSON编码）
	config := zap.NewProductionConfig()

	// 设置日志级别
	switch level {
	case "debug":
		config.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		config.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		config.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		config.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	// 配置输出到 stdout
	config.OutputPaths = []string{"stdout"}
	config.ErrorOutputPaths = []string{"stderr"}

	// 自定义时间格式
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// 自定义 caller 编码格式，显示相对路径和行号
	config.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder

	// 保持 caller 信息启用
	config.DisableCaller = false

	// 创建 logger
	logger, err := config.Build(
		zap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return &funcCore{Core: core}
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	// 添加 caller skip，如果 skip > 0
	if skip > 0 {
		logger = logger.WithOptions(zap.AddCallerSkip(skip))
	}

	return logger, nil
}

// WithCallerSkip 为现有的 logger 添加 caller skip
// 这个函数允许在使用时动态调整 caller 层级
func WithCallerSkip(logger *zap.Logger, skip int) *zap.Logger {
	if logger == nil || skip <= 0 {
		return logger
	}
	return logger.WithOptions(zap.AddCallerSkip(skip))
}

// funcCore 是一个自定义的 Core，自动添加函数名字段
type funcCore struct {
	zapcore.Core
}

func (c *funcCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *funcCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// 获取调用栈，需要跳过更多层到真实的业务代码
	// 跳过层级：Write -> funcCore.Write -> zap.Logger.Info -> 业务代码
	pc, _, _, ok := runtime.Caller(4)
	if ok {
		fn := runtime.FuncForPC(pc)
		if fn != nil {
			funcName := fn.Name()
			// 添加函数名字段
			fields = append(fields, zap.String("func", funcName))
		}
	}

	// 调用原始的 Write
	return c.Core.Write(entry, fields)
}

func (c *funcCore) With(fields []zapcore.Field) zapcore.Core {
	clone := c.Core.With(fields)
	return &funcCore{Core: clone}
}
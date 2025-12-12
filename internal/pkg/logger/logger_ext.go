package logger

import (
	"go.uber.org/zap"
)

// Logger 扩展 zap.Logger，提供链式调用调整 caller skip
type Logger struct {
	*zap.Logger
}

// NewLogger 创建一个新的扩展 logger
func NewLogger(base *zap.Logger) *Logger {
	return &Logger{Logger: base}
}

// Skip 返回一个新的 Logger，跳过指定层数的调用栈
func (l *Logger) Skip(skip int) *Logger {
	if skip <= 0 {
		return l
	}
	return &Logger{
		Logger: l.Logger.WithOptions(zap.AddCallerSkip(skip)),
	}
}

// SkipOne 返回一个新的 Logger，跳过一层调用栈
func (l *Logger) SkipOne() *Logger {
	return l.Skip(1)
}

// SkipTwo 返回一个新的 Logger，跳过两层调用栈
func (l *Logger) SkipTwo() *Logger {
	return l.Skip(2)
}

// Wrap 将 zap.Logger 包装成扩展 Logger
func Wrap(zapLogger *zap.Logger) *Logger {
	return &Logger{Logger: zapLogger}
}

// 下面的方法提供链式调用支持

// Debug logs a message at DebugLevel with caller skip support
func (l *Logger) Debug(msg string, fields ...zap.Field) {
	// Skip 1 level to skip this wrapper method
	l.Logger.WithOptions(zap.AddCallerSkip(1)).Debug(msg, fields...)
}

// Info logs a message at InfoLevel with caller skip support
func (l *Logger) Info(msg string, fields ...zap.Field) {
	// Skip 1 level to skip this wrapper method
	l.Logger.WithOptions(zap.AddCallerSkip(1)).Info(msg, fields...)
}

// Warn logs a message at WarnLevel with caller skip support
func (l *Logger) Warn(msg string, fields ...zap.Field) {
	// Skip 1 level to skip this wrapper method
	l.Logger.WithOptions(zap.AddCallerSkip(1)).Warn(msg, fields...)
}

// Error logs a message at ErrorLevel with caller skip support
func (l *Logger) Error(msg string, fields ...zap.Field) {
	// Skip 1 level to skip this wrapper method
	l.Logger.WithOptions(zap.AddCallerSkip(1)).Error(msg, fields...)
}

// Fatal logs a message at FatalLevel with caller skip support
func (l *Logger) Fatal(msg string, fields ...zap.Field) {
	// Skip 1 level to skip this wrapper method
	l.Logger.WithOptions(zap.AddCallerSkip(1)).Fatal(msg, fields...)
}

// With adds a field to the logger and returns a new Logger
func (l *Logger) With(fields ...zap.Field) *Logger {
	return &Logger{
		Logger: l.Logger.With(fields...),
	}
}

// Named adds a name to the logger and returns a new Logger
func (l *Logger) Named(name string) *Logger {
	return &Logger{
		Logger: l.Logger.Named(name),
	}
}
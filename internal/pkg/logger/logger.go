package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// logFile is the default on-disk log path. Logs are written here in addition to
// stdout so they persist across restarts, and the file is rotated by size/age
// via lumberjack (see RotationConfig) so it cannot grow unbounded.
const logFile = "./logs/aigis.log"

// RotationConfig configures on-disk logging. Rotation via lumberjack is OPT-IN:
// it is only used when Enabled is true. Left disabled (the default), logs are
// written to a single file and rotation is left to an external tool such as
// logrotate — running both lumberjack and logrotate against the same file would
// conflict (logrotate renames/truncates the file out from under lumberjack's fd).
type RotationConfig struct {
	Enabled    bool   // use lumberjack rotation; false = single file (logrotate-friendly)
	Filename   string // log file path
	MaxSizeMB  int    // rotate after the file exceeds this many megabytes
	MaxBackups int    // max number of rotated files to retain
	MaxAgeDays int    // max age in days before a rotated file is deleted
	Compress   bool   // gzip rotated files
}

// DefaultRotation returns the defaults: rotation DISABLED (single file), with
// sane lumberjack parameters that apply only once Enabled is set to true.
func DefaultRotation() RotationConfig {
	return RotationConfig{
		Enabled:    false,
		Filename:   logFile,
		MaxSizeMB:  100,
		MaxBackups: 7,
		MaxAgeDays: 30,
		Compress:   true,
	}
}

// New 创建一个新的 zap logger实例
// level: 日志级别 (debug, info, warn, error)
// 返回配置好的 logger 和可能的错误
func New(level string) (*zap.Logger, error) {
	return newLogger(level, 0, DefaultRotation())
}

// NewWithRotation 创建一个带自定义滚动配置的 logger（serve.go 用它从 config 注入）
func NewWithRotation(level string, rot RotationConfig) (*zap.Logger, error) {
	return newLogger(level, 0, rot)
}

// NewWithCallerSkip 创建一个新的 zap logger实例，并设置 caller skip
// level: 日志级别 (debug, info, warn, error)
// skip: 跳过的调用栈层数
// 返回配置好的 logger 和可能的错误
func NewWithCallerSkip(level string, skip int) (*zap.Logger, error) {
	return newLogger(level, skip, DefaultRotation())
}

// levelFromString maps a config level string to a zap level (defaults to info).
func levelFromString(level string) zapcore.Level {
	switch level {
	case "debug":
		return zap.DebugLevel
	case "info":
		return zap.InfoLevel
	case "warn":
		return zap.WarnLevel
	case "error":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}

// configureEncoder applies the shared timestamp/caller formatting so the
// rotating and static log paths stay byte-for-byte consistent.
func configureEncoder(cfg *zapcore.EncoderConfig) {
	cfg.TimeKey = "timestamp"
	cfg.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncodeCaller = zapcore.ShortCallerEncoder
}

// newLogger builds the logger, choosing the on-disk strategy by rot.Enabled:
// lumberjack rotation when set, otherwise a single file (rotation delegated to
// an external tool like logrotate). stdout is always written too.
func newLogger(level string, skip int, rot RotationConfig) (*zap.Logger, error) {
	if rot.Filename == "" {
		rot.Filename = logFile
	}
	// 确保日志目录存在
	if err := os.MkdirAll(filepath.Dir(rot.Filename), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create logs dir: %w", err)
	}

	var (
		logger *zap.Logger
		err    error
	)
	if rot.Enabled {
		logger, err = buildRotatingLogger(level, rot)
	} else {
		logger, err = buildStaticLogger(level, rot.Filename)
	}
	if err != nil {
		return nil, err
	}

	if skip > 0 {
		logger = logger.WithOptions(zap.AddCallerSkip(skip))
	}
	return logger, nil
}

// buildStaticLogger is the original (non-rotating) implementation: zap writes
// stdout + a single file via its production config. Rotation, if wanted, is the
// job of an external tool (logrotate). This is the default path.
func buildStaticLogger(level, filename string) (*zap.Logger, error) {
	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(levelFromString(level))
	config.OutputPaths = []string{"stdout", filename}
	config.ErrorOutputPaths = []string{"stderr", filename}
	configureEncoder(&config.EncoderConfig)
	config.DisableCaller = false

	logger, err := config.Build(
		zap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return &funcCore{Core: core}
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}
	return logger, nil
}

// buildRotatingLogger tees a stdout core and a lumberjack-backed rotating-file
// core. Cores are constructed directly (not via a registered zap sink) so the
// rotation config is honored per call and tests can target a temp dir.
func buildRotatingLogger(level string, rot RotationConfig) (*zap.Logger, error) {
	encCfg := zap.NewProductionEncoderConfig()
	configureEncoder(&encCfg)
	encoder := zapcore.NewJSONEncoder(encCfg)

	lvl := zap.NewAtomicLevelAt(levelFromString(level))

	roller := &lumberjack.Logger{
		Filename:   rot.Filename,
		MaxSize:    rot.MaxSizeMB,
		MaxBackups: rot.MaxBackups,
		MaxAge:     rot.MaxAgeDays,
		Compress:   rot.Compress,
	}

	// 同时写 stdout（实时调试/容器）和滚动文件（持久化 + 自动切割）
	core := zapcore.NewTee(
		zapcore.NewCore(encoder, zapcore.Lock(os.Stdout), lvl),
		zapcore.NewCore(encoder, zapcore.AddSync(roller), lvl),
	)
	// funcCore 自动附加函数名字段（保留原行为）
	return zap.New(&funcCore{Core: core}, zap.AddCaller()), nil
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
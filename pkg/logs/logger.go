package logs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/natefinch/lumberjack"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	// gLogger is the main application logger; guarded by mu because it is
	// initialised once via sync.Once and read back through GetGlobalLogger.
	gLogger *Logger
	once    sync.Once
	mu      sync.RWMutex

	// The behavior logger globals are written exactly once at startup (and
	// once more to nil during Close).  Using atomic.Pointer lets behaviorLog
	// read them on every HTTP request without acquiring any lock, while
	// still being race-safe with the rare Close() call.
	gBehaviorLogger atomic.Pointer[zap.Logger]
	gBehaviorBuffer atomic.Pointer[zapcore.BufferedWriteSyncer]
	gBehaviorLumber atomic.Pointer[lumberjack.Logger]
)

// Logger wraps zap.Logger with buffered output and optional log-rotation.
type Logger struct {
	zapLogger *zap.Logger
	buffer    *zapcore.BufferedWriteSyncer
	lumber    *lumberjack.Logger
	config    Config
}

// GetGlobalLogger returns the singleton logger instance.
func GetGlobalLogger() *Logger {
	mu.RLock()
	defer mu.RUnlock()
	return gLogger
}

// NewLogger initialises the global logger exactly once.
func NewLogger(cfg Config) *Logger {
	once.Do(func() {
		gLogger = createLogger(cfg)
	})
	return gLogger
}

func createLogger(cfg Config) *Logger {
	logger := &Logger{config: cfg}

	cfg.Mode = strings.ToLower(cfg.Mode)
	var writer zapcore.WriteSyncer
	if cfg.Mode == "file" {
		var lumber *lumberjack.Logger
		writer, lumber = getFileWriter(cfg)
		logger.lumber = lumber
	} else {
		writer = getConsoleWriter()
	}

	if writer == nil {
		return logger
	}

	logger.buffer = newBufferedWriteSyncer(writer, cfg)

	core := zapcore.NewCore(NewLogEncoder(cfg.ServiceName), logger.buffer, parseLevel(cfg.Level))
	logger.zapLogger = zap.New(core,
		zap.AddCaller(),
		zap.AddCallerSkip(2),
		zap.AddStacktrace(zap.PanicLevel),
	)
	// 创建行为日志
	initBehaviorLogger(cfg)

	return logger
}

func initBehaviorLogger(cfg Config) {
	if cfg.FileInfo.PathBehavior == "" {
		return
	}
	behaviorWriter, behaviorLumber := getBehaviorFileWriter(cfg)
	if behaviorWriter == nil {
		return
	}
	gBehaviorLumber.Store(behaviorLumber)
	bb := newBufferedWriteSyncer(behaviorWriter, cfg)
	gBehaviorBuffer.Store(bb)
	behaviorCore := zapcore.NewCore(NewLogEncoder(cfg.ServiceName), bb, parseLevel(cfg.Level))
	gBehaviorLogger.Store(zap.New(behaviorCore,
		zap.AddCaller(),
		zap.AddCallerSkip(1),
		zap.AddStacktrace(zap.PanicLevel),
	))
}

func newBufferedWriteSyncer(ws zapcore.WriteSyncer, cfg Config) *zapcore.BufferedWriteSyncer {
	size := cfg.BufferSize * 1024 * 1024
	if size <= 0 {
		size = 256 * 1024 // 256KB default
	}
	interval := time.Duration(cfg.BufferFlushInterval) * time.Millisecond
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &zapcore.BufferedWriteSyncer{
		WS:            ws,
		Size:          size,
		FlushInterval: interval,
	}
}

// parseLevel maps a level string to a zapcore.Level, defaulting to Info.
func parseLevel(level string) zapcore.Level {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// Close flushes and releases all resources held by the global logger
// (main logger and behavior logger).  Safe to call multiple times.
func Close() {
	mu.Lock()
	l := gLogger
	gLogger = nil
	mu.Unlock()

	if l != nil {
		if l.buffer != nil {
			_ = l.buffer.Stop()
		}
		if l.zapLogger != nil {
			_ = l.zapLogger.Sync()
		}
		if l.lumber != nil {
			_ = l.lumber.Close()
		}
	}
	// Shutdown order for the behavior logger:
	//   1. Detach the zap.Logger so no new entries can be enqueued.
	//   2. Sync it to push any in-flight entries into the buffer.
	//   3. Stop the buffer to flush it to the underlying file writer.
	//   4. Close the file (lumberjack) to release the file descriptor.
	// Reversing steps 1-2 and 3 (the previous order) caused bl.Sync() to write
	// into an already-stopped BufferedWriteSyncer, silently discarding entries.
	if bl := gBehaviorLogger.Swap(nil); bl != nil {
		_ = bl.Sync()
	}
	if bb := gBehaviorBuffer.Swap(nil); bb != nil {
		_ = bb.Stop()
	}
	if blumber := gBehaviorLumber.Swap(nil); blumber != nil {
		_ = blumber.Close()
	}
}

// Flush forces any buffered log entries to be written.
func (l *Logger) Flush() {
	if l == nil || l.zapLogger == nil {
		return
	}
	_ = l.zapLogger.Sync()
}

// Close flushes the logger and closes the underlying file writer if present.
func (l *Logger) Close() error {
	if l == nil || l.zapLogger == nil {
		return nil
	}
	l.Flush()
	if l.lumber != nil {
		return l.lumber.Close()
	}
	return nil
}

func (l *Logger) Fatal(ctx context.Context, msg string, format ...any) {
	l.logAndExit(ctx, msg, format, 1)
}

func (l *Logger) Fatalf(ctx context.Context, format string, args ...any) {
	l.logAndExit(ctx, fmt.Sprintf(format, args...), nil, 1)
}

func (l *Logger) Fatalln(ctx context.Context, args ...any) {
	l.logAndExit(ctx, fmt.Sprintln(args...), nil, 1)
}

func (l *Logger) Panic(ctx context.Context, msg string, format ...any) {
	l.logAndPanic(ctx, msg, format)
}

func (l *Logger) Panicf(ctx context.Context, format string, args ...any) {
	l.logAndPanic(ctx, fmt.Sprintf(format, args...), nil)
}

func (l *Logger) Panicln(ctx context.Context, args ...any) {
	l.logAndPanic(ctx, fmt.Sprintln(args...), nil)
}

func (l *Logger) logAndExit(ctx context.Context, msg string, format []any, code int) {
	if l != nil {
		writeLog(ctx, l, zap.ErrorLevel, msg, format...)
		l.Flush()
	}
	os.Exit(code)
}

func (l *Logger) logAndPanic(ctx context.Context, msg string, format []any) {
	if l != nil {
		writeLog(ctx, l, zap.ErrorLevel, msg, format...)
		l.Flush()
	}
	panic(msg)
}

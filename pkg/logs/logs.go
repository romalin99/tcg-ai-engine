package logs

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"tcg-ai-engine/pkg/constant"
)

// appendContextFields extracts user_id and trace_id from ctx.Value.
func appendContextFields(fields []zapcore.Field, ctx context.Context) []zapcore.Field {
	if v, ok := ctx.Value(constant.CtxUserID).(string); ok && v != "" {
		fields = append(fields, zap.String(string(constant.CtxUserID), v))
	}
	if v, ok := ctx.Value(constant.CtxTraceID).(string); ok && v != "" {
		fields = append(fields, zap.String(string(constant.CtxTraceID), v))
	}
	return fields
}

func writeLog(ctx context.Context, logger *Logger, level zapcore.Level, msg string, format ...any) {
	if logger == nil || logger.zapLogger == nil {
		return
	}

	var fields []zapcore.Field
	if len(format) == 0 {
		// Fast path: no formatting or extra fields. Allocate the fields slice
		// lazily so callers with context.Background() (the common case) incur
		// zero heap allocations here.
		fields = appendContextFields(fields, ctx)
	} else {
		// Slow path: format the message and lift any zap.Field args out of format.
		fields = appendContextFields(make([]zapcore.Field, 0, len(format)+2), ctx)
		var extraFields []zapcore.Field
		msg, extraFields = formatMsg(msg, format...)
		fields = append(fields, extraFields...)
	}

	switch level {
	case zap.DebugLevel:
		logger.zapLogger.Debug(msg, fields...)
	case zap.InfoLevel:
		logger.zapLogger.Info(msg, fields...)
	case zap.WarnLevel:
		logger.zapLogger.Warn(msg, fields...)
	case zap.ErrorLevel:
		logger.zapLogger.Error(msg, fields...)
	}
}

// formatMsg separates zapcore.Field arguments from printf-style format arguments.
func formatMsg(format string, a ...any) (string, []zapcore.Field) {
	if len(a) == 0 {
		// No args at all — return the format string verbatim without any formatting.
		return format, nil
	}

	// Scan once to check whether any arg is a zapcore.Field.
	// Most callers pass only printf-style args, so we avoid the two helper-slice
	// allocations unless zap fields are actually present.
	hasField := false
	for _, arg := range a {
		if _, ok := arg.(zapcore.Field); ok {
			hasField = true
			break
		}
	}
	if !hasField {
		// All args are printf-style; skip the separation loop entirely.
		return fmt.Sprintf(format, a...), nil
	}

	// Slow path: separate zapcore.Field args from printf-style args.
	args := make([]any, 0, len(a))
	fields := make([]zapcore.Field, 0, len(a))
	for _, arg := range a {
		if field, ok := arg.(zapcore.Field); ok {
			fields = append(fields, field)
		} else {
			args = append(args, arg)
		}
	}
	return fmt.Sprintf(format, args...), fields
}

// Any wraps an arbitrary value as a named zap field.
func Any(key string, data any) zap.Field {
	return zap.Any(key, data)
}

// Flag attaches a free-form flag string to a log entry.
func Flag(flag string) zap.Field {
	return zap.String("flag", flag)
}

func Debug(ctx context.Context, msg string, format ...any) {
	writeLog(ctx, gLogger, zap.DebugLevel, msg, format...)
}

func Info(ctx context.Context, msg string, format ...any) {
	writeLog(ctx, gLogger, zap.InfoLevel, msg, format...)
}

func Warn(ctx context.Context, msg string, format ...any) {
	writeLog(ctx, gLogger, zap.WarnLevel, msg, format...)
}

func Err(ctx context.Context, msg string, format ...any) {
	writeLog(ctx, gLogger, zap.ErrorLevel, msg, format...)
}

func Flush() {
	mu.RLock()
	l := gLogger
	mu.RUnlock()

	if l != nil {
		if l.buffer != nil {
			_ = l.buffer.Sync()
		}
		if l.zapLogger != nil {
			_ = l.zapLogger.Sync()
		}
	}
	if bb := gBehaviorBuffer.Load(); bb != nil {
		_ = bb.Sync()
	}
	if bl := gBehaviorLogger.Load(); bl != nil {
		_ = bl.Sync()
	}
}

func Fatal(ctx context.Context, msg string, format ...any) {
	writeLog(ctx, gLogger, zap.ErrorLevel, msg, format...)
	Flush()
	os.Exit(1)
}

func Fatalf(ctx context.Context, format string, args ...any) {
	writeLog(ctx, gLogger, zap.ErrorLevel, fmt.Sprintf(format, args...))
	Flush()
	os.Exit(1)
}

func Fatalln(ctx context.Context, args ...any) {
	writeLog(ctx, gLogger, zap.ErrorLevel, fmt.Sprintln(args...))
	Flush()
	os.Exit(1)
}

func Panic(ctx context.Context, msg string, format ...any) {
	writeLog(ctx, gLogger, zap.ErrorLevel, msg, format...)
	doPanic(msg)
}

func Panicf(ctx context.Context, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	writeLog(ctx, gLogger, zap.ErrorLevel, msg)
	doPanic(msg)
}

func Panicln(ctx context.Context, args ...any) {
	msg := fmt.Sprintln(args...)
	writeLog(ctx, gLogger, zap.ErrorLevel, msg)
	doPanic(msg)
}

func doPanic(msg string) {
	Flush()
	panic(msg)
}

// behaviorLog logs to the behavior file only (API request logs).
// No-op if behavior logger is not initialised (PathBehavior empty).
// Lock-free: gBehaviorLogger is an atomic.Pointer so no mutex is acquired on
// this hot path (called on every HTTP request).
func behaviorLog(level zapcore.Level, msg string) {
	bl := gBehaviorLogger.Load()
	if bl == nil {
		return
	}
	switch level {
	case zapcore.InfoLevel:
		bl.Info(msg)
	case zapcore.WarnLevel:
		bl.Warn(msg)
	case zapcore.ErrorLevel:
		bl.Error(msg)
	}
}

// BehaviorInfo logs to the behavior file only (API request logs).
func BehaviorInfo(msg string) { behaviorLog(zapcore.InfoLevel, msg) }

// BehaviorWarn logs to the behavior file only.
func BehaviorWarn(msg string) { behaviorLog(zapcore.WarnLevel, msg) }

// BehaviorError logs to the behavior file only.
func BehaviorError(msg string) { behaviorLog(zapcore.ErrorLevel, msg) }

package logs

import (
	"fmt"
	"math"
	"strings"
	"time"

	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

// levelPadding maps level strings to their pre-padded versions (5 chars wide),
// avoiding per-call string padding in the hot EncodeEntry path.
var levelPadding = map[string]string{
	"DEBUG":  "DEBUG",
	"INFO":   "INFO ",
	"WARN":   "WARN ",
	"ERROR":  "ERROR",
	"DPANIC": "DPANIC",
	"PANIC":  "PANIC",
	"FATAL":  "FATAL",
}

// LogEncoder is a custom zapcore.Encoder that formats log entries with
// service name, caller location, and structured fields as key=value pairs.
type LogEncoder struct {
	zapcore.Encoder
	bufferPool  buffer.Pool
	serviceName string
}

var defaultEncoderConfig = zapcore.EncoderConfig{
	TimeKey:       "time",
	LevelKey:      "level",
	NameKey:       "logger",
	CallerKey:     "caller",
	MessageKey:    "msg",
	StacktraceKey: "stack",
	EncodeTime:    zapcore.ISO8601TimeEncoder,
	EncodeLevel:   zapcore.CapitalLevelEncoder,
	EncodeCaller:  zapcore.ShortCallerEncoder,
	LineEnding:    zapcore.DefaultLineEnding,
}

// NewLogEncoder returns a LogEncoder that tags every log line with serviceName.
func NewLogEncoder(serviceName string) zapcore.Encoder {
	return &LogEncoder{
		Encoder:     zapcore.NewConsoleEncoder(defaultEncoderConfig),
		bufferPool:  buffer.NewPool(),
		serviceName: serviceName,
	}
}

// Clone returns a deep copy of the encoder, safe for concurrent use.
// The bufferPool is shared with the parent encoder: buffer.Pool is a value
// type wrapping a *sync.Pool, so sharing it is correct and avoids creating
// a new free-list per clone (which would increase GC pressure under high
// log volume).
func (e *LogEncoder) Clone() zapcore.Encoder {
	return &LogEncoder{
		Encoder:     e.Encoder.Clone(),
		bufferPool:  e.bufferPool,
		serviceName: e.serviceName,
	}
}

// EncodeEntry encodes a log entry and its fields into a pooled buffer.
// Output format:
//
//	[timestamp] [service] [LEVEL] [file:line] - message | key=value, ...
//
// A stack trace is appended on a new line when present.
//
// Performance: uses direct buffer writes instead of fmt.Fprintf to eliminate
// reflection-based format parsing and intermediate string allocations on every
// log call (high-frequency hot path).
func (e *LogEncoder) EncodeEntry(ent zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	buf := e.bufferPool.Get()
	buf.Reset()

	// [timestamp]
	buf.AppendByte('[')
	buf.AppendString(ent.Time.Format("2006-01-02 15:04:05.000"))
	buf.AppendString("] [")

	// [service]
	buf.AppendString(e.serviceName)
	buf.AppendString("] [")

	// [LEVEL] — left-padded to 5 chars; use pre-built map to avoid runtime padding
	levelStr := ent.Level.CapitalString()
	if padded, ok := levelPadding[levelStr]; ok {
		buf.AppendString(padded)
	} else {
		buf.AppendString(levelStr)
		for i := len(levelStr); i < 5; i++ {
			buf.AppendByte(' ')
		}
	}
	buf.AppendString("] [")

	// [dir/file:line] — 保留上一级目录便于在同名文件(如多个 client.go)间区分。
	buf.AppendString(trimmedFile(ent.Caller.File))
	buf.AppendByte(':')
	buf.AppendInt(int64(ent.Caller.Line))
	buf.AppendString("] - ")

	// message
	buf.AppendString(ent.Message)

	if len(fields) > 0 {
		buf.AppendString(" | ")
		e.encodeFields(buf, fields)
	}
	if ent.Stack != "" {
		buf.AppendString("\nStack trace:\n")
		buf.AppendString(ent.Stack)
	}
	buf.AppendByte('\n')
	return buf, nil
}

// trimmedFile 返回路径末尾两段(目录/文件),例如:
//
//	/Users/.../internal/middleware/logger.go → middleware/logger.go
//	/Users/.../pkg/logs/encoder.go           → logs/encoder.go
//
// 与 zapcore.EntryCaller.TrimmedPath() 行为一致,但只返回路径部分;行号由
// 调用方分别拼接,避免一次字符串拼接和 strconv 分配。
//
// 实现:从末尾扫描两个 '/',返回从第二个 '/' 后开始的子串(切片操作,零拷贝)。
// 不到两段则原样返回。仅按 '/' 切分 —— Go 的 runtime.Caller 在所有平台
// (含 Windows) 均返回 forward slash。
func trimmedFile(p string) string {
	idx := strings.LastIndexByte(p, '/')
	if idx < 0 {
		return p
	}
	idx2 := strings.LastIndexByte(p[:idx], '/')
	if idx2 < 0 {
		return p
	}
	return p[idx2+1:]
}

// encodeFields writes fields directly into buf as a comma-separated list of
// key=value pairs, avoiding intermediate string allocations.
func (e *LogEncoder) encodeFields(buf *buffer.Buffer, fields []zapcore.Field) {
	first := true
	for _, f := range fields {
		if f.Type == zapcore.SkipType {
			continue
		}
		if !first {
			buf.AppendString(", ")
		}
		first = false
		buf.AppendString(f.Key)
		buf.AppendByte('=')
		switch f.Type {
		case zapcore.StringType:
			buf.AppendString(f.String)
		case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type,
			zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type,
			zapcore.UintptrType:
			buf.AppendInt(f.Integer)
		case zapcore.Float32Type:
			val := math.Float32frombits(uint32(f.Integer))
			buf.AppendFloat(float64(val), 32)
		case zapcore.Float64Type:
			val := math.Float64frombits(uint64(f.Integer))
			buf.AppendFloat(val, 64)
		case zapcore.BoolType:
			buf.AppendBool(f.Integer != 0)
		case zapcore.DurationType:
			buf.AppendString(time.Duration(f.Integer).String())
		case zapcore.StringerType:
			if s, ok := f.Interface.(fmt.Stringer); ok {
				buf.AppendString(s.String())
			}
		default:
			if f.Interface != nil {
				buf.AppendString(fmt.Sprintf("%v", f.Interface))
			}
		}
	}
}

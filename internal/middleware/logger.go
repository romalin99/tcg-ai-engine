package middleware

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"tcg-ai-engine/pkg/constant"
	"tcg-ai-engine/pkg/logs"
)

// maxLoggedBody 行为日志里请求体的截断上限；ucs-fe 用 masking 做脱敏，
// 本项目请求体是订单/客户 Fact，暂以截断防日志膨胀，需要脱敏时再引入 masking。
const maxLoggedBody = 2048

// msgBuilderPool reuses strings.Builder instances across requests to eliminate
// per-request heap allocations in the behavior log hot path (called on every
// non-skipped HTTP request).
var msgBuilderPool = sync.Pool{
	New: func() any {
		sb := &strings.Builder{}
		sb.Grow(256) // pre-size for a typical behavior log line
		return sb
	},
}

// extractTraceIDs returns the traceID and spanID for the current request.
// It checks the OTel span first, then falls back to well-known HTTP headers.
func extractTraceIDs(ctx context.Context, c fiber.Ctx) (traceID, spanID string) {
	if span := trace.SpanFromContext(ctx); span != nil && span.SpanContext().IsValid() {
		sc := span.SpanContext()
		return sc.TraceID().String(), sc.SpanID().String()
	}

	if c != nil {
		// X-App-Trace-ID is the canonical application trace header.
		traceID = c.Get("X-App-Trace-ID")
		spanID = c.Get("X-Span-Id")
		if traceID == "" {
			traceID = c.Get("X-Trace-Id")
		}
		if traceID == "" {
			traceID = c.Get("uber-trace-id")
		}
		if traceID == "" {
			traceID = c.Get("traceparent")
		}
	}
	if traceID == "" {
		traceID = "unknown"
	}
	if spanID == "" {
		spanID = "unknown"
	}
	return traceID, spanID
}

// BehaviorLogger logs every non-skipped HTTP request to the behavior log file.
type BehaviorLogger struct {
	serviceName  string
	skipPrefixes []string
}

func NewBehaviorLogger(serviceName string) *BehaviorLogger {
	return &BehaviorLogger{
		serviceName: serviceName,
		skipPrefixes: []string{
			"/metrics",
			"/swagger",
			"/favicon.ico",
			"/health",
			"/livez",
			"/readyz",
			"/ping",
			"/monitor",
		},
	}
}

func (l *BehaviorLogger) Handle() fiber.Handler {
	return func(c fiber.Ctx) error {
		path := c.Path()
		for _, prefix := range l.skipPrefixes {
			if strings.HasPrefix(path, prefix) {
				return c.Next()
			}
		}

		// X-App-Trace-ID 优先，调用方可用自己的 trace token 关联日志；
		// 没有时才回退到 OTel span 的 trace ID。
		traceID := c.Get("X-App-Trace-ID")
		if traceID == "" {
			traceID, _ = extractTraceIDs(c.Context(), c)
		}
		c.SetContext(context.WithValue(c.Context(), constant.CtxTraceID, traceID))

		// Log the incoming request before the handler runs.
		l.logIncomingRequest(c)

		start := time.Now()
		err := c.Next()

		// Echo the trace token back in the response so callers can correlate
		// their own response logs with the upstream request chain.
		c.Set("X-App-Trace-ID", traceID)

		l.logRequest(c, start)
		return err
	}
}

// logIncomingRequest writes one structured log line at the moment the request
// arrives, before any handler logic runs.
func (l *BehaviorLogger) logIncomingRequest(c fiber.Ctx) {
	ctx := c.Context()
	if body := c.Body(); len(body) > 0 {
		logs.Info(ctx, "[API-REQUEST] [START] method=%s uri=%s body=%s addr=%s",
			c.Method(), c.OriginalURL(), truncateBody(body), getClientIP(c))
	} else {
		logs.Info(ctx, "[API-REQUEST] [START] method=%s uri=%s addr=%s",
			c.Method(), c.OriginalURL(), getClientIP(c))
	}
}

func truncateBody(body []byte) string {
	if len(body) <= maxLoggedBody {
		return string(body)
	}
	return string(body[:maxLoggedBody]) + "...(truncated)"
}

func (l *BehaviorLogger) logRequest(c fiber.Ctx, start time.Time) {
	ctx := c.Context()
	traceID, spanID := extractTraceIDs(ctx, c)

	statusCode := c.Response().StatusCode()
	elapsed := time.Since(start)

	// Structured response log — ctx carries trace_id so the field is appended
	// automatically by appendContextFields on every logs.Info/Warn/Err call.
	logMsg := "[API-RESPONSE] [END] method=%s uri=%s status=%d elapsed=%dms addr=%s body=%s"
	logArgs := []any{c.Method(), c.OriginalURL(), statusCode, elapsed.Milliseconds(), getClientIP(c), truncateBody(c.Response().Body())}
	switch {
	case statusCode >= 500:
		logs.Err(ctx, logMsg, logArgs...)
	case statusCode >= 400:
		logs.Warn(ctx, logMsg, logArgs...)
	default:
		logs.Info(ctx, logMsg, logArgs...)
	}

	// Use a pooled builder to avoid a heap allocation from fmt.Sprintf on
	// every request.
	sb := msgBuilderPool.Get().(*strings.Builder)
	sb.Reset()
	sb.WriteString("[")
	sb.WriteString(traceID)
	sb.WriteString("/")
	sb.WriteString(spanID)
	sb.WriteString("] [API-REQUEST] URI: ")
	sb.WriteString(c.Path())
	sb.WriteString(", Method: ")
	sb.WriteString(c.Method())
	sb.WriteString(", Status: ")
	sb.WriteString(strconv.Itoa(statusCode))
	sb.WriteString(", Addr: ")
	sb.WriteString(getClientIP(c))
	sb.WriteString(", Elapsed: ")
	sb.WriteString(strconv.FormatInt(elapsed.Milliseconds(), 10))
	sb.WriteString("ms")
	msg := sb.String()
	msgBuilderPool.Put(sb)

	switch {
	case statusCode >= 500:
		logs.BehaviorError(msg)
	case statusCode >= 400:
		logs.BehaviorWarn(msg)
	default:
		logs.BehaviorInfo(msg)
	}
}

// getClientIP returns the client's real IP address.
// It prefers the rightmost public IP in X-Forwarded-For to resist spoofing,
// then falls back to X-Real-IP and finally to the direct remote address.
func getClientIP(c fiber.Ctx) string {
	if xff := strings.TrimSpace(c.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if ip := strings.TrimSpace(parts[i]); isPublicIP(ip) {
				return ip
			}
		}
	}

	if xrip := strings.TrimSpace(c.Get("X-Real-IP")); xrip != "" && isPublicIP(xrip) {
		return xrip
	}

	return c.IP()
}

// isPublicIP returns true when ip is a valid, globally-routable address.
func isPublicIP(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return addr.IsGlobalUnicast() && !addr.IsPrivate()
}

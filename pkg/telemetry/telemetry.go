package telemetry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"

	"tcg-ai-engine/pkg/logs"
)

// Telemetry holds the OpenTelemetry tracing configuration and provider.
type Telemetry struct {
	tp         *sdktrace.TracerProvider
	ServerName string   `mapstructure:"server_name"`
	Endpoint   string   `mapstructure:"endpoint"`
	Batcher    string   `mapstructure:"batcher"`
	SkipPaths  []string `mapstructure:"skip_paths"`
	Sampler    float64  `mapstructure:"sampler"`
	Enabled    bool     `mapstructure:"enabled"`
}

// Validate checks that the telemetry configuration values are valid.
func (t *Telemetry) Validate() error {
	if t.Sampler < 0 || t.Sampler > 1 {
		return fmt.Errorf("telemetry.sampler must be between 0 and 1")
	}
	switch t.Batcher {
	case "otlp", "none":
		return nil
	default:
		return fmt.Errorf("unsupported telemetry.batcher: %s", t.Batcher)
	}
}

// silentErrorHandler suppresses expected network errors when the
// telemetry backend (e.g. Jaeger) is temporarily unreachable.
type silentErrorHandler struct{}

func (silentErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "dial tcp") ||
		strings.Contains(msg, "traces export") {
		return
	}
	logs.Warn(context.Background(), "[telemetry] exporter error: %v", err)
}

// InitTracer initializes the global OpenTelemetry tracer provider using OTLP HTTP.
// Jaeger 1.35+ natively accepts OTLP on port 4318, no Jaeger-side changes required.
func (t *Telemetry) InitTracer() *sdktrace.TracerProvider {
	ctx := context.Background()

	if !t.Enabled {
		logs.Info(ctx, "telemetry disabled, skipping tracer init")
		return nil
	}

	// suppress connection-refused noise from the background exporter goroutine
	otel.SetErrorHandler(silentErrorHandler{})

	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(t.Endpoint),
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled:         true,
			InitialInterval: 1 * time.Second,
			MaxInterval:     10 * time.Second,
			MaxElapsedTime:  30 * time.Second,
		}),
	)
	if err != nil {
		logs.Warn(ctx, "telemetry unavailable, tracing disabled: %v", err)
		return nil
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(t.Sampler)),
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(t.ServerName),
		)),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	t.tp = tp
	logs.Info(ctx, "telemetry initialized, endpoint=%s sampler=%.2f", t.Endpoint, t.Sampler)
	return tp
}

// Close shuts down the tracer provider gracefully, flushing any pending spans.
// A 5-second timeout prevents the shutdown from blocking indefinitely when
// the OTLP/Jaeger backend is unreachable, which would delay the shutdown of
// all downstream components (DB, Redis, Kafka) and risk log buffer truncation.
func (t *Telemetry) Close() {
	if t.tp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := t.tp.Shutdown(ctx); err != nil {
		logs.Warn(context.Background(), "error shutting down tracer provider: %v", err)
	}
}

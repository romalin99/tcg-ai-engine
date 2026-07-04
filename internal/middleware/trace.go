package middleware

import (
	"slices"

	fiberotel "github.com/gofiber/contrib/v3/otel"
	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// OtelConfig holds configuration for the OpenTelemetry tracing middleware.
type OtelConfig struct {
	SkipPaths []string
}

// EnableOtelTrace returns a Fiber middleware that instruments requests with OTel traces.
func EnableOtelTrace(cfg OtelConfig) fiber.Handler {
	return fiberotel.Middleware(
		fiberotel.WithTracerProvider(otel.GetTracerProvider()),
		fiberotel.WithPropagators(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		)),
		fiberotel.WithSpanNameFormatter(func(c fiber.Ctx) string {
			return c.Method() + " " + c.Path()
		}),
		fiberotel.WithNext(func(c fiber.Ctx) bool {
			return slices.Contains(cfg.SkipPaths, c.Path())
		}),
	)
}

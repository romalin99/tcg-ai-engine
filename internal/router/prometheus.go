package router

import (
	"github.com/gofiber/contrib/monitor"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/gofiber/fiber/v3/middleware/healthcheck"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"tcg-ai-engine/internal/config"
	"tcg-ai-engine/internal/middleware"
)

func RegisterPrometheus(app *fiber.App, c *config.Config) {
	app.Get("/monitor", monitor.New())

	fiberProm := middleware.NewFiberPrometheus(c.Log.ServiceName,
		middleware.WithSkipPaths([]string{"/ping", "/swagger", "/favicon.ico", "/metrics", "/monitor"}),
		middleware.WithIgnoreStatusCodes([]int{401, 403, 404}),
	)
	app.Use(fiberProm.Middleware)

	// Build the Prometheus handler once and reuse it on the route.
	metricsHandler := adaptor.HTTPHandler(promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{EnableOpenMetrics: true},
	))
	app.Get("/metrics", metricsHandler)

	// Register liveness and readiness on explicit paths with a single
	// healthcheck instance; avoids intercepting unmatched GET requests.
	hc := healthcheck.New()
	app.Get(healthcheck.LivenessEndpoint, hc)
	app.Get(healthcheck.ReadinessEndpoint, hc)
}

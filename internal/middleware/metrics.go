package middleware

import (
	"slices"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/prometheus/client_golang/prometheus"
)

type FiberPrometheus struct {
	requestsTotal     *prometheus.CounterVec
	requestDuration   *prometheus.HistogramVec
	requestSize       *prometheus.SummaryVec
	responseSize      *prometheus.SummaryVec
	activeRequests    *prometheus.GaugeVec
	serviceName       string
	skipPaths         []string
	ignoreStatusCodes []int
}

// PrometheusOption configures a FiberPrometheus instance.
type PrometheusOption func(*FiberPrometheus)

// WithSkipPaths sets paths that bypass all metrics collection.
func WithSkipPaths(paths []string) PrometheusOption {
	return func(fp *FiberPrometheus) { fp.skipPaths = paths }
}

// WithIgnoreStatusCodes sets HTTP status codes that are not recorded in metrics.
func WithIgnoreStatusCodes(codes []int) PrometheusOption {
	return func(fp *FiberPrometheus) { fp.ignoreStatusCodes = codes }
}

// mustRegisterOrGet registers collector with the default Registerer.
// If the metric was already registered (e.g. in tests that create multiple
// FiberPrometheus instances), it returns the previously registered collector
// instead of panicking.  This mirrors the pattern recommended by the
// prometheus/client_golang authors for reusable components.
func mustRegisterOrGet[C prometheus.Collector](c C) C {
	if err := prometheus.Register(c); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(C)
		}
		panic(err)
	}
	return c
}

func NewFiberPrometheus(serviceName string, opts ...PrometheusOption) *FiberPrometheus {
	fp := &FiberPrometheus{
		serviceName:       serviceName,
		skipPaths:         []string{},
		ignoreStatusCodes: []int{},
	}

	labels := []string{"method", "path", "status"}
	constLabels := prometheus.Labels{"service": serviceName}

	fp.requestsTotal = mustRegisterOrGet(prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name:        "http_requests_total",
			Help:        "Total number of HTTP requests",
			ConstLabels: constLabels,
		},
		labels,
	))

	fp.requestDuration = mustRegisterOrGet(prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:        "http_request_duration_seconds",
			Help:        "HTTP request duration in seconds",
			ConstLabels: constLabels,
			Buckets:     []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		labels,
	))

	fp.requestSize = mustRegisterOrGet(prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:        "http_request_size_bytes",
			Help:        "HTTP request size in bytes",
			ConstLabels: constLabels,
		},
		labels,
	))

	fp.responseSize = mustRegisterOrGet(prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name:        "http_response_size_bytes",
			Help:        "HTTP response size in bytes",
			ConstLabels: constLabels,
		},
		labels,
	))

	// Labelled by method only — deliberately NOT by path. The in-flight observer
	// must be created before c.Next() (when only the raw, un-normalised path is
	// available), and a GaugeVec never releases a child series once created, so
	// labelling by raw path would accumulate one permanent series per distinct
	// URL ever seen (404 scans, path params, …) — an unbounded memory leak.
	fp.activeRequests = mustRegisterOrGet(prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name:        "http_requests_in_progress",
			Help:        "Current number of HTTP requests being processed",
			ConstLabels: constLabels,
		},
		[]string{"method"},
	))

	for _, opt := range opts {
		opt(fp)
	}
	return fp
}

func (fp *FiberPrometheus) Middleware(c fiber.Ctx) error {
	path := c.Path()

	if slices.Contains(fp.skipPaths, path) {
		return c.Next()
	}

	method := c.Method()

	// Track in-flight requests by method only (see GaugeVec definition above for
	// why path is intentionally omitted). The observer is captured before
	// c.Next() so that Inc and the deferred Dec reference the same label set.
	activeGauge := fp.activeRequests.WithLabelValues(method)
	activeGauge.Inc()
	defer activeGauge.Dec()

	start := time.Now()
	err := c.Next()
	duration := time.Since(start).Seconds()

	status := c.Response().StatusCode()
	if slices.Contains(fp.ignoreStatusCodes, status) {
		return err
	}

	// Use the matched route pattern instead of the raw path to prevent
	// high-cardinality label explosion from arbitrary/malicious URLs. Unmatched
	// requests (404 scans, bad/random URLs) carry no route pattern; bucket them
	// under a single "unmatched" label so a scanner cannot create an unbounded
	// number of permanent metric series (CounterVec/HistogramVec/SummaryVec
	// children are never released once observed).
	if rp := c.Route().Path; rp != "" {
		path = rp
	} else {
		path = "unmatched"
	}

	statusStr := strconv.Itoa(status)

	// Pass label values as direct variadic arguments instead of constructing a
	// []string slice.  This avoids a heap allocation on every request because
	// the compiler can place the three-element variadic on the stack.
	fp.requestsTotal.WithLabelValues(method, path, statusStr).Inc()
	fp.requestDuration.WithLabelValues(method, path, statusStr).Observe(duration)
	fp.requestSize.WithLabelValues(method, path, statusStr).Observe(float64(len(c.Request().Body())))
	fp.responseSize.WithLabelValues(method, path, statusStr).Observe(float64(len(c.Response().Body())))

	return err
}

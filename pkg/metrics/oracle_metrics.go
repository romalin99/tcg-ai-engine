package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// DbPoolMaxOpen reports the configured maximum number of open connections in the pool
	DbPoolMaxOpen *prometheus.GaugeVec
	// DbPoolOpen reports the current number of open connections in the pool
	DbPoolOpen *prometheus.GaugeVec
	// DbPoolInUse reports how many connections are currently in use
	DbPoolInUse *prometheus.GaugeVec
	// DbPoolIdle reports how many connections are currently idle
	DbPoolIdle *prometheus.GaugeVec

	// DbPoolWaitCount reports the cumulative number of waits for a connection
	DbPoolWaitCount *prometheus.GaugeVec
	// DbPoolWaitDuration reports the cumulative wait time for connections in seconds
	DbPoolWaitDuration *prometheus.GaugeVec
	// DbPoolMaxIdleClosed reports how many connections were closed for exceeding max idle limits
	DbPoolMaxIdleClosed *prometheus.GaugeVec
	// DbPoolMaxLifetimeClosed reports how many connections were closed for exceeding max lifetime limits
	DbPoolMaxLifetimeClosed *prometheus.GaugeVec
	// DbPoolUsageRate reports the pool usage rate as a percentage
	DbPoolUsageRate *prometheus.GaugeVec

	onceDBMetrics sync.Once
)

// InitDBMetrics registers repository connection pool metrics and scopes them by service.
func InitDBMetrics(serviceName string) {
	onceDBMetrics.Do(func() {
		// Use a registerer wrapped with the service label
		reg := prometheus.WrapRegistererWith(
			prometheus.Labels{"service": serviceName},
			prometheus.DefaultRegisterer,
		)

		DbPoolMaxOpen = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_max_open_connections",
				Help: "Maximum number of open connections in the DB pool",
			},
			[]string{"instance"},
		)

		DbPoolOpen = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_open_connections",
				Help: "Current number of open connections in the DB pool",
			},
			[]string{"instance"},
		)

		DbPoolInUse = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_in_use",
				Help: "Number of connections currently in use",
			},
			[]string{"instance"},
		)

		DbPoolIdle = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_idle",
				Help: "Number of idle connections in the DB pool",
			},
			[]string{"instance"},
		)

		DbPoolWaitCount = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_wait_count_total",
				Help: "Total number of waits for a connection (cumulative)",
			},
			[]string{"instance"},
		)

		DbPoolWaitDuration = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_wait_duration_seconds_total",
				Help: "Total wait duration for connections in seconds (cumulative)",
			},
			[]string{"instance"},
		)

		DbPoolMaxIdleClosed = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_max_idle_closed_total",
				Help: "Total number of connections closed due to max idle (cumulative)",
			},
			[]string{"instance"},
		)

		DbPoolMaxLifetimeClosed = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_max_lifetime_closed_total",
				Help: "Total number of connections closed due to max lifetime (cumulative)",
			},
			[]string{"instance"},
		)

		DbPoolUsageRate = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "db_pool_usage_rate_percent",
				Help: "Usage rate of the DB pool (percentage)",
			},
			[]string{"instance"},
		)

		reg.MustRegister(
			DbPoolMaxOpen,
			DbPoolOpen,
			DbPoolInUse,
			DbPoolIdle,
			DbPoolWaitCount,
			DbPoolWaitDuration,
			DbPoolMaxIdleClosed,
			DbPoolMaxLifetimeClosed,
			DbPoolUsageRate,
		)
	})
}

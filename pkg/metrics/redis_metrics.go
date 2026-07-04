package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	RedisMaxPoolSize    *prometheus.GaugeVec
	RedisTotalConns     *prometheus.GaugeVec
	RedisIdleConns      *prometheus.GaugeVec
	RedisWaitDurationNs *prometheus.GaugeVec
	RedisHits           *prometheus.CounterVec
	RedisMisses         *prometheus.CounterVec
	RedisTimeouts       *prometheus.CounterVec
	RedisWaitCount      *prometheus.CounterVec
	RedisStaleConns     *prometheus.CounterVec

	onceRedisMetrics sync.Once
)

// InitRedisMetrics registers Redis connection pool metrics for the service.
func InitRedisMetrics(serviceName string) {
	onceRedisMetrics.Do(func() {
		// Use a registerer wrapped with the service label (consistent with oracle/kafka metrics)
		reg := prometheus.WrapRegistererWith(
			prometheus.Labels{"service": serviceName},
			prometheus.DefaultRegisterer,
		)

		RedisMaxPoolSize = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "redis_max_pool_size",
				Help: "Configured maximum Redis connection pool size.",
			},
			[]string{"pool"},
		)

		RedisTotalConns = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "redis_total_connections",
				Help: "Current total number of Redis connections (active + idle).",
			},
			[]string{"pool"},
		)

		RedisIdleConns = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "redis_idle_connections",
				Help: "Current number of idle Redis connections in the pool.",
			},
			[]string{"pool"},
		)

		RedisHits = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "redis_hits_total",
				Help: "Total number of Redis connection pool hits.",
			},
			[]string{"pool"},
		)

		RedisMisses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "redis_misses_total",
				Help: "Total number of Redis connection pool misses.",
			},
			[]string{"pool"},
		)

		RedisTimeouts = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "redis_timeouts_total",
				Help: "Total number of Redis connection pool timeouts.",
			},
			[]string{"pool"},
		)

		RedisWaitCount = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "redis_wait_count_total",
				Help: "Total number of times waiting for a Redis connection.",
			},
			[]string{"pool"},
		)

		RedisWaitDurationNs = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "redis_wait_duration_ns_total",
				Help: "Total wait duration for Redis connections in nanoseconds.",
			},
			[]string{"pool"},
		)

		RedisStaleConns = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "redis_stale_conns_total",
				Help: "Total number of stale (closed or expired) Redis connections.",
			},
			[]string{"pool"},
		)

		reg.MustRegister(
			RedisMaxPoolSize,
			RedisTotalConns,
			RedisIdleConns,
			RedisHits,
			RedisMisses,
			RedisTimeouts,
			RedisWaitCount,
			RedisWaitDurationNs,
			RedisStaleConns,
		)
	})
}

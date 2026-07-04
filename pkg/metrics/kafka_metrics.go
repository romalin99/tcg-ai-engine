package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// KafkaCommitSuccessTotal counts successful Kafka offset commits.
	KafkaCommitSuccessTotal *prometheus.CounterVec
	// KafkaCommitFailuresTotal counts commits that failed after retries.
	KafkaCommitFailuresTotal *prometheus.CounterVec
	// KafkaCommitRetriesTotal counts commit retry attempts.
	KafkaCommitRetriesTotal *prometheus.CounterVec
	// KafkaCommitConsecutiveFailures tracks consecutive commit failures.
	KafkaCommitConsecutiveFailures *prometheus.GaugeVec

	onceKafkaMetrics sync.Once
)

// InitKafkaMetrics registers Kafka commit metrics for the service.
func InitKafkaMetrics(serviceName string) {
	onceKafkaMetrics.Do(func() {
		reg := prometheus.WrapRegistererWith(
			prometheus.Labels{"service": serviceName},
			prometheus.DefaultRegisterer,
		)

		KafkaCommitSuccessTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kafka_commit_success_total",
				Help: "Total number of successful Kafka offset commits",
			},
			[]string{"group", "topic"},
		)

		KafkaCommitFailuresTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kafka_commit_failures_total",
				Help: "Total number of Kafka offset commits that failed after retries",
			},
			[]string{"group", "topic"},
		)

		KafkaCommitRetriesTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kafka_commit_retries_total",
				Help: "Total number of Kafka offset commit retry attempts",
			},
			[]string{"group", "topic"},
		)

		KafkaCommitConsecutiveFailures = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "kafka_commit_consecutive_failures",
				Help: "Current number of consecutive Kafka offset commit failures",
			},
			[]string{"group", "topic"},
		)

		reg.MustRegister(
			KafkaCommitSuccessTotal,
			KafkaCommitFailuresTotal,
			KafkaCommitRetriesTotal,
			KafkaCommitConsecutiveFailures,
		)
	})
}

// KafkaMetricsEnabled reports whether Kafka metrics have been initialized.
func KafkaMetricsEnabled() bool {
	return KafkaCommitSuccessTotal != nil
}

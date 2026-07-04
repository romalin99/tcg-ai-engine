package metrics

// Init wires up the default metrics pipelines for the service.
func Init(serviceName string) {
	InitDBMetrics(serviceName)
	InitRedisMetrics(serviceName)
	InitKafkaMetrics(serviceName)
}

package kafka

import (
	"context"
	"time"

	"tcg-ai-engine/pkg/logs"
)

var (
	KafkaProducer *Producer
	KafkaConsumer *Consumer
)

// ---------------------------------------------------------------
// Producer config
// ---------------------------------------------------------------

// ProducerConfig holds global producer configuration with optional per-topic overrides.
type ProducerConfig struct {
	Topics               map[string]ProducerTopicConfig `mapstructure:"topics"`
	ClientID             string                         `mapstructure:"client_id"`
	Topic                string                         `mapstructure:"default_topic"`
	Compression          string                         `mapstructure:"compression"`
	SASL                 ProducerSASLConfig             `mapstructure:"sasl"`
	TLS                  ProducerTLSConfig              `mapstructure:"tls"`
	Brokers              []string                       `mapstructure:"brokers"`
	DefaultTopics        []string                       `mapstructure:"default_topics"`
	RecordRetries        int                            `mapstructure:"record_retries"`
	MaxBufferedRecords   int                            `mapstructure:"max_buffered_records"`
	RequiredAcks         int                            `mapstructure:"required_acks"`
	MetricsLogIntervalMs int                            `mapstructure:"metrics_log_interval_ms"`
	RetryQueueCap        int                            `mapstructure:"retry_queue_cap"`
	RequestTimeoutMs     int                            `mapstructure:"request_timeout_ms"`
	DeliveryTimeoutMs    int                            `mapstructure:"delivery_timeout_ms"`
	FlushTimeoutMs       int                            `mapstructure:"flush_timeout_ms"`
	LingerMs             int                            `mapstructure:"linger_ms"`
	MaxBufferedBytes     int                            `mapstructure:"max_buffered_bytes"`
	MaxInFlight          int                            `mapstructure:"max_in_flight"`
	BatchMaxBytes        int32                          `mapstructure:"batch_max_bytes"`
	HardDeliveryBound    bool                           `mapstructure:"hard_delivery_bound"`
	EnableIdempotence    bool                           `mapstructure:"enable_idempotence"`
}

// ProducerTLSConfig maps [kafka.producer.tls]. mTLS requires cert_file and key_file
// together; enabling SASL without TLS transmits credentials in plaintext.
type ProducerTLSConfig struct {
	CAFile             string `mapstructure:"ca_file"`
	CertFile           string `mapstructure:"cert_file"`
	KeyFile            string `mapstructure:"key_file"`
	Enable             bool   `mapstructure:"enable"`
	InsecureSkipVerify bool   `mapstructure:"insecure_skip_verify"`
}

// ProducerSASLConfig maps [kafka.producer.sasl].
// Mechanism: "" | PLAIN | SCRAM-SHA-256 | SCRAM-SHA-512. Prefer pass_file over pass.
type ProducerSASLConfig struct {
	Mechanism string `mapstructure:"mechanism"`
	User      string `mapstructure:"user"`
	Pass      string `mapstructure:"pass"`
	PassFile  string `mapstructure:"pass_file"`
	Enable    bool   `mapstructure:"enable"`
}

// ProducerTopicConfig holds per-topic producer overrides.
// Zero values are ignored; ProducerConfig global defaults apply instead.
type ProducerTopicConfig struct {
	Acks            string `mapstructure:"acks"`
	Compression     string `mapstructure:"compression"`
	Retries         int    `mapstructure:"retries"`
	RetryBackoffMs  int    `mapstructure:"retry_backoff_ms"`
	LingerMs        int    `mapstructure:"linger_ms"`
	BatchSize       int32  `mapstructure:"batch_size"`
	MaxMessageBytes int32  `mapstructure:"max_message_bytes"`
}

// EffectiveTopicConfig merges global defaults with per-topic overrides.
func (c *ProducerConfig) EffectiveTopicConfig(topic string) ProducerTopicConfig {
	base := ProducerTopicConfig{
		Compression:     c.Compression,
		MaxMessageBytes: c.batchMaxBytes(),
		LingerMs:        c.LingerMs,
	}

	override, ok := c.Topics[topic]
	if !ok {
		return base
	}
	if override.Compression != "" {
		base.Compression = override.Compression
	}
	if override.MaxMessageBytes > 0 {
		base.MaxMessageBytes = override.MaxMessageBytes
	}
	if override.LingerMs > 0 {
		base.LingerMs = override.LingerMs
	}
	if override.BatchSize > 0 {
		base.BatchSize = override.BatchSize
	}
	if override.Acks != "" {
		base.Acks = override.Acks
	}
	if override.Retries > 0 {
		base.Retries = override.Retries
	}
	if override.RetryBackoffMs > 0 {
		base.RetryBackoffMs = override.RetryBackoffMs
	}
	return base
}

func (c *ProducerConfig) batchMaxBytes() int32 {
	if c.BatchMaxBytes > 0 {
		return c.BatchMaxBytes
	}
	return 1024 * 1024 // 1MB
}

// RequestTimeout returns the producer request timeout as a Duration.
func (c *ProducerConfig) RequestTimeout() time.Duration {
	if c.RequestTimeoutMs > 0 {
		return time.Duration(c.RequestTimeoutMs) * time.Millisecond
	}
	return 30 * time.Second
}

// ---------------------------------------------------------------
// Consumer config
// ---------------------------------------------------------------

// ConsumerConfig holds global consumer configuration with optional per-topic overrides.
type ConsumerConfig struct {
	Topics map[string]*ConsumerTopicConfig `mapstructure:"topics"`

	ClientID        string   `mapstructure:"client_id"`
	GroupID         string   `mapstructure:"group_id"`
	AutoOffsetReset string   `mapstructure:"auto_offset_reset"`
	Brokers         []string `mapstructure:"brokers"`
	DefaultTopics   []string `mapstructure:"default_topics"`

	MaxPollRecords int `mapstructure:"max_poll_records"`

	// fetch tuning
	FetchMaxBytes          int32 `mapstructure:"fetch_max_bytes"`
	FetchMaxPartitionBytes int32 `mapstructure:"fetch_max_partition_bytes"`
	FetchMinBytes          int32 `mapstructure:"fetch_min_bytes"`
	FetchMaxWaitMs         int   `mapstructure:"fetch_max_wait_ms"`

	// session / heartbeat / rebalance (milliseconds)
	SessionTimeoutMs    int `mapstructure:"session_timeout_ms"`
	HeartbeatIntervalMs int `mapstructure:"heartbeat_interval_ms"`
	RebalanceTimeoutMs  int `mapstructure:"rebalance_timeout_ms"`

	// commit / shutdown (milliseconds)
	CommitTimeoutMs      int `mapstructure:"commit_timeout_ms"`
	ShutdownTimeoutMs    int `mapstructure:"shutdown_timeout_ms"`
	RevokeFlushTimeoutMs int `mapstructure:"revoke_flush_timeout_ms"`
	MaxWorkers           int `mapstructure:"max_workers"`
}

// ConsumerTopicConfig holds per-topic consumer overrides.
// Zero values are ignored; ConsumerConfig global defaults apply instead.
type ConsumerTopicConfig struct {
	ClientID        string `mapstructure:"client_id"`
	GroupID         string `mapstructure:"group_id"`
	AutoOffsetReset string `mapstructure:"auto_offset_reset"`

	// fetch tuning
	FetchMaxBytes          int32 `mapstructure:"fetch_max_bytes"`
	FetchMaxPartitionBytes int32 `mapstructure:"fetch_max_partition_bytes"`
	FetchMinBytes          int32 `mapstructure:"fetch_min_bytes"`
	FetchMaxWaitMs         int   `mapstructure:"fetch_max_wait_ms"`
	MaxConcurrentFetches   int   `mapstructure:"max_concurrent_fetches"`
	FetchErrorBackoffMs    int   `mapstructure:"fetch_error_backoff_ms"`
	MaxWorkers             int   `mapstructure:"max_workers"`

	// retry
	MaxRetries     int `mapstructure:"max_retries"`
	CommitMaxRetry int `mapstructure:"commit_max_retry"`

	// commit (manual batch-commit model)
	CommitBatchSize   int `mapstructure:"commit_batch_size"`
	CommitChanCap     int `mapstructure:"commit_chan_cap"`
	CommitTimeoutMs   int `mapstructure:"commit_timeout_ms"`
	CommitBlockWarnMs int `mapstructure:"commit_block_warn_ms"`

	// session / heartbeat / rebalance / shutdown (milliseconds)
	SessionTimeoutMs     int `mapstructure:"session_timeout_ms"`
	HeartbeatIntervalMs  int `mapstructure:"heartbeat_interval_ms"`
	RebalanceTimeoutMs   int `mapstructure:"rebalance_timeout_ms"`
	ShutdownTimeoutMs    int `mapstructure:"shutdown_timeout_ms"`
	RevokeFlushTimeoutMs int `mapstructure:"revoke_flush_timeout_ms"`
}

// EffectiveTopicConsumerConfig merges global defaults with per-topic overrides.
// Zero-valued overrides are ignored; per-topic-only fields are taken verbatim.
func (c *ConsumerConfig) EffectiveTopicConsumerConfig(topic string) ConsumerTopicConfig {
	base := ConsumerTopicConfig{
		ClientID:               c.ClientID,
		GroupID:                c.GroupID,
		AutoOffsetReset:        normalizeOffsetReset(c.AutoOffsetReset),
		FetchMaxBytes:          c.FetchMaxBytes,
		FetchMaxPartitionBytes: c.FetchMaxPartitionBytes,
		FetchMinBytes:          c.FetchMinBytes,
		FetchMaxWaitMs:         c.FetchMaxWaitMs,
		SessionTimeoutMs:       c.SessionTimeoutMs,
		HeartbeatIntervalMs:    c.HeartbeatIntervalMs,
		RebalanceTimeoutMs:     c.RebalanceTimeoutMs,
		CommitTimeoutMs:        c.CommitTimeoutMs,
		ShutdownTimeoutMs:      c.ShutdownTimeoutMs,
		RevokeFlushTimeoutMs:   c.RevokeFlushTimeoutMs,
		MaxWorkers:             c.MaxWorkers,
	}

	override, ok := c.Topics[topic]
	if !ok || override == nil {
		return base
	}

	if override.ClientID != "" {
		base.ClientID = override.ClientID
	}
	if override.GroupID != "" {
		base.GroupID = override.GroupID
	}
	if override.AutoOffsetReset != "" {
		base.AutoOffsetReset = normalizeOffsetReset(override.AutoOffsetReset)
	}
	if override.FetchMaxBytes > 0 {
		base.FetchMaxBytes = override.FetchMaxBytes
	}
	if override.FetchMaxPartitionBytes > 0 {
		base.FetchMaxPartitionBytes = override.FetchMaxPartitionBytes
	}
	if override.FetchMinBytes > 0 {
		base.FetchMinBytes = override.FetchMinBytes
	}
	if override.FetchMaxWaitMs > 0 {
		base.FetchMaxWaitMs = override.FetchMaxWaitMs
	}
	if override.SessionTimeoutMs > 0 {
		base.SessionTimeoutMs = override.SessionTimeoutMs
	}
	if override.HeartbeatIntervalMs > 0 {
		base.HeartbeatIntervalMs = override.HeartbeatIntervalMs
	}
	if override.RebalanceTimeoutMs > 0 {
		base.RebalanceTimeoutMs = override.RebalanceTimeoutMs
	}
	if override.CommitTimeoutMs > 0 {
		base.CommitTimeoutMs = override.CommitTimeoutMs
	}
	if override.ShutdownTimeoutMs > 0 {
		base.ShutdownTimeoutMs = override.ShutdownTimeoutMs
	}
	if override.RevokeFlushTimeoutMs > 0 {
		base.RevokeFlushTimeoutMs = override.RevokeFlushTimeoutMs
	}
	if override.MaxWorkers > 0 {
		base.MaxWorkers = override.MaxWorkers
	}

	// per-topic-only fields (no global counterpart)
	base.MaxConcurrentFetches = override.MaxConcurrentFetches
	base.MaxRetries = override.MaxRetries
	base.CommitMaxRetry = override.CommitMaxRetry
	base.CommitBatchSize = override.CommitBatchSize
	base.CommitChanCap = override.CommitChanCap
	base.CommitBlockWarnMs = override.CommitBlockWarnMs
	base.FetchErrorBackoffMs = override.FetchErrorBackoffMs

	logs.Info(context.Background(), "Kafka consumer effective topic: %s, config=%+v", topic, base)
	return base
}

// SessionTimeout returns session timeout as a Duration.
func (c *ConsumerConfig) SessionTimeout() time.Duration {
	if c.SessionTimeoutMs > 0 {
		return time.Duration(c.SessionTimeoutMs) * time.Millisecond
	}
	return 10 * time.Second
}

// HeartbeatInterval returns heartbeat interval as a Duration.
func (c *ConsumerConfig) HeartbeatInterval() time.Duration {
	if c.HeartbeatIntervalMs > 0 {
		return time.Duration(c.HeartbeatIntervalMs) * time.Millisecond
	}
	return 3 * time.Second
}

// FetchMaxBytesOrDefault returns fetch_max_bytes with a sensible default.
func (c *ConsumerConfig) FetchMaxBytesOrDefault() int32 {
	if c.FetchMaxBytes > 0 {
		return c.FetchMaxBytes
	}
	return 50 * 1024 * 1024 // 50MB
}

// MaxPollRecordsOrDefault returns max_poll_records with a sensible default.
func (c *ConsumerConfig) MaxPollRecordsOrDefault() int {
	if c.MaxPollRecords > 0 {
		return c.MaxPollRecords
	}
	return 100
}

// normalizeOffsetReset maps "newest"/"oldest" to "latest"/"earliest" for consistency.
func normalizeOffsetReset(v string) string {
	switch v {
	case "newest":
		return "latest"
	case "oldest":
		return "earliest"
	case "":
		return "latest"
	default:
		return v
	}
}

// ---------------------------------------------------------------
// Root config + Init
// ---------------------------------------------------------------

// Config is the root Kafka configuration, mapped from toml [kafka].
type Config struct {
	Consumer ConsumerConfig `mapstructure:"consumer"`
	Producer ProducerConfig `mapstructure:"producer"`
}

// InitProducer initialises the global KafkaProducer singleton.
func (k *Config) InitProducer() *Producer {
	if KafkaProducer != nil {
		return KafkaProducer
	}
	// default_topics is a list (mirrors [kafka.consumer]); kgo produces to a single
	// default topic, so the first entry is used. An explicit default_topic wins.
	if k.Producer.Topic == "" && len(k.Producer.DefaultTopics) > 0 {
		k.Producer.Topic = k.Producer.DefaultTopics[0]
	}
	if len(k.Producer.Brokers) > 0 {
		KafkaProducer = NewProducer(&k.Producer)
	}
	return KafkaProducer
}

// InitConsumer initialises the global KafkaConsumer singleton.
func (k *Config) InitConsumer() *Consumer {
	if KafkaConsumer != nil {
		return KafkaConsumer
	}
	if len(k.Consumer.Brokers) > 0 {
		KafkaConsumer = NewConsumer(&k.Consumer)
	}
	return KafkaConsumer
}

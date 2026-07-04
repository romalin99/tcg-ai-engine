package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"tcg-ai-engine/pkg/logs"
)

// Producer wraps kgo.Client for producing Kafka messages.
type Producer struct {
	client *kgo.Client
	cfg    *ProducerConfig
}

// NewProducer creates a Producer using the provided ProducerConfig.
// It applies global defaults merged with the default topic's per-topic overrides,
// wires TLS/SASL when configured, and enforces acks=all + idempotence by default.
// Returns nil if the config is invalid or the broker connection cannot be established.
func NewProducer(cfg *ProducerConfig) *Producer {
	if cfg == nil || len(cfg.Brokers) == 0 {
		logs.Fatal(context.Background(), "kafka producer config is invalid")
		return nil
	}

	opts, err := buildProducerOpts(cfg)
	if err != nil {
		logs.Err(context.Background(), "build kafka producer options failed: %v", err)
		return nil
	}

	client, err := kgo.NewClient(opts...)
	if err != nil {
		logs.Err(context.Background(), "create kafka producer failed: %v", err)
		return nil
	}

	// verify broker connectivity at startup
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		client.Close()
		logs.Err(context.Background(), "kafka producer ping failed: %v", err)
		return nil
	}

	logs.Info(context.Background(),
		"kafka producer initialized: brokers=%v clientID=%s topic=%s tls=%t sasl=%t idempotent=%t hardDeliveryBound=%t partitioner=sticky-key",
		cfg.Brokers, cfg.ClientID, cfg.defaultTopic(), cfg.TLS.Enable, cfg.SASL.Enable, cfg.EnableIdempotence, cfg.HardDeliveryBound)

	return &Producer{client: client, cfg: cfg}
}

// buildProducerOpts constructs the kgo.Opt slice from ProducerConfig, merging global
// defaults with the default topic's per-topic overrides and wiring TLS/SASL.
func buildProducerOpts(cfg *ProducerConfig) ([]kgo.Opt, error) {
	topicCfg := cfg.EffectiveTopicConfig(cfg.defaultTopic())

	tlsCfg, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}
	mech, err := buildSASL(cfg.SASL)
	if err != nil {
		return nil, err
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.DefaultProduceTopic(cfg.defaultTopic()),
		kgo.WithLogger(kgoLogger{}),

		// metadata: detect leader migration faster than the 5m default — mitigates
		// head-of-line blocking on keyed partitions when a leader goes down.
		kgo.MetadataMaxAge(time.Minute),
		kgo.MetadataMinAge(5 * time.Second),
		kgo.DialTimeout(10 * time.Second),

		// batching / throughput
		kgo.ProducerBatchCompression(topicCfg.compressionCodec()...),
		kgo.ProducerBatchMaxBytes(topicCfg.batchMaxBytes()),
		kgo.ProducerLinger(topicCfg.lingerDuration()),

		// retry / timeouts
		kgo.RecordRetries(cfg.recordRetries(topicCfg)),
		kgo.RetryBackoffFn(topicCfg.retryBackoff()),
		kgo.RecordDeliveryTimeout(cfg.DeliveryTimeout()),
		kgo.ProduceRequestTimeout(cfg.RequestTimeout()),

		// StickyKeyPartitioner: records with a key hash to a stable partition
		// (murmur2 % n), so the same key always lands on the same partition → per-key
		// ordering. Keyed records require leader availability (HOL on leader loss).
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
	}

	// Reliability: idempotence requires acks=all. With enable_idempotence=true (the
	// safe default) we enforce acks=all and keep idempotent writes on. Setting
	// enable_idempotence=false explicitly opts out and honours required_acks instead.
	if cfg.EnableIdempotence {
		opts = append(opts, kgo.RequiredAcks(kgo.AllISRAcks()))
		if cfg.HardDeliveryBound {
			// Without this, RecordDeliveryTimeout / RecordRetries do NOT bound an
			// already-in-flight idempotent record (the client waits indefinitely to
			// dedup). AllowIdempotentProduceCancellation makes them a hard bound and
			// lets the delivery callback fire on timeout/retry exhaustion.
			// Trade-off: a cancelled record re-sent by the app may duplicate →
			// consumers must dedup by (player_id, occurred_at).
			opts = append(opts, kgo.AllowIdempotentProduceCancellation())
		}
	} else {
		opts = append(opts, kgo.RequiredAcks(topicCfg.requiredAcks()), kgo.DisableIdempotentWrite())
	}

	if cfg.ClientID != "" {
		opts = append(opts, kgo.ClientID(cfg.ClientID))
	}
	if cfg.MaxBufferedRecords > 0 {
		opts = append(opts, kgo.MaxBufferedRecords(cfg.MaxBufferedRecords))
	}
	if cfg.MaxBufferedBytes > 0 {
		opts = append(opts, kgo.MaxBufferedBytes(cfg.MaxBufferedBytes))
	}
	if tlsCfg != nil {
		opts = append(opts, kgo.DialTLSConfig(tlsCfg))
	}
	if mech != nil {
		opts = append(opts, kgo.SASL(mech))
	}

	return opts, nil
}

// buildTLSConfig builds a *tls.Config from [kafka.producer.tls]; returns nil when disabled.
func buildTLSConfig(t ProducerTLSConfig) (*tls.Config, error) {
	if !t.Enable {
		return nil, nil
	}
	tc := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: t.InsecureSkipVerify, //nolint:gosec // only when explicitly enabled (test only)
	}
	if t.CAFile != "" {
		ca, err := os.ReadFile(t.CAFile) //nolint:gosec // path from trusted config
		if err != nil {
			return nil, fmt.Errorf("read tls ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("no certificates parsed from ca_file %q", t.CAFile)
		}
		tc.RootCAs = pool
	}
	// mTLS: cert and key must be provided together, else the handshake fails opaquely.
	if (t.CertFile == "") != (t.KeyFile == "") {
		return nil, errors.New("tls cert_file and key_file must be set together (mTLS)")
	}
	if t.CertFile != "" && t.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load tls client cert/key (mTLS): %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}

// buildSASL builds a sasl.Mechanism from [kafka.producer.sasl]; returns nil when disabled.
func buildSASL(s ProducerSASLConfig) (sasl.Mechanism, error) {
	if !s.Enable {
		return nil, nil
	}
	pass := s.Pass
	if s.PassFile != "" {
		b, err := os.ReadFile(s.PassFile) //nolint:gosec // path from trusted config (k8s secret mount)
		if err != nil {
			return nil, fmt.Errorf("read sasl pass_file: %w", err)
		}
		pass = strings.TrimSpace(string(b))
	}
	if s.User == "" || pass == "" {
		return nil, errors.New("sasl enabled but user/pass empty")
	}
	switch strings.ToUpper(strings.TrimSpace(s.Mechanism)) {
	case "PLAIN":
		return plain.Auth{User: s.User, Pass: pass}.AsMechanism(), nil
	case "SCRAM-SHA-256":
		return scram.Auth{User: s.User, Pass: pass}.AsSha256Mechanism(), nil
	case "SCRAM-SHA-512":
		return scram.Auth{User: s.User, Pass: pass}.AsSha512Mechanism(), nil
	default:
		return nil, fmt.Errorf("unsupported sasl mechanism %q (PLAIN|SCRAM-SHA-256|SCRAM-SHA-512)", s.Mechanism)
	}
}

// SendMessage sends a message asynchronously to the specified topic.
func (p *Producer) SendMessage(topic, key, value string) {
	p.client.Produce(
		context.Background(),
		&kgo.Record{
			Topic:     topic,
			Key:       []byte(key),
			Value:     []byte(value),
			Timestamp: time.Now(),
		},
		func(r *kgo.Record, err error) {
			if err != nil {
				logs.Err(context.Background(),
					"send message to kafka failed: topic=%s key=%s err=%v",
					r.Topic, string(r.Key), err)
			}
		},
	)
}

// SendMessageSync sends a message synchronously and waits for acknowledgment.
func (p *Producer) SendMessageSync(ctx context.Context, topic, key, value string) error {
	record := &kgo.Record{
		Topic:     topic,
		Key:       []byte(key),
		Value:     []byte(value),
		Timestamp: time.Now(),
	}
	if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		return fmt.Errorf("sync produce failed topic=%s key=%s: %w", topic, key, err)
	}
	return nil
}

// Close flushes buffered records (bounded by flush_timeout_ms) and closes the producer.
func (p *Producer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.FlushTimeout())
	defer cancel()

	if err := p.client.Flush(ctx); err != nil {
		logs.Warn(ctx, "kafka producer flush failed: %v", err)
	}
	p.client.Close()
	logs.Info(ctx, "kafka producer closed")
}

// ---------------------------------------------------------------
// ProducerConfig helpers
// ---------------------------------------------------------------

// defaultTopic resolves the single produce topic: explicit default_topic wins,
// otherwise the first entry of default_topics (kgo produces to one default topic).
func (c *ProducerConfig) defaultTopic() string {
	if c.Topic != "" {
		return c.Topic
	}
	if len(c.DefaultTopics) > 0 {
		return c.DefaultTopics[0]
	}
	return ""
}

// DeliveryTimeout returns the per-record delivery timeout (default 30s).
func (c *ProducerConfig) DeliveryTimeout() time.Duration {
	if c.DeliveryTimeoutMs > 0 {
		return time.Duration(c.DeliveryTimeoutMs) * time.Millisecond
	}
	return 30 * time.Second
}

// FlushTimeout returns the shutdown flush bound (default DeliveryTimeout + 5s).
func (c *ProducerConfig) FlushTimeout() time.Duration {
	if c.FlushTimeoutMs > 0 {
		return time.Duration(c.FlushTimeoutMs) * time.Millisecond
	}
	return c.DeliveryTimeout() + 5*time.Second
}

// recordRetries prefers an explicit per-topic retries override, then the global
// record_retries, then a default of 5.
func (c *ProducerConfig) recordRetries(t ProducerTopicConfig) int {
	if t.Retries > 0 {
		return t.Retries
	}
	if c.RecordRetries > 0 {
		return c.RecordRetries
	}
	return 5
}

// ---------------------------------------------------------------
// ProducerTopicConfig helpers
// ---------------------------------------------------------------

// requiredAcks converts the acks string to a kgo.Acks value.
func (t *ProducerTopicConfig) requiredAcks() kgo.Acks {
	switch t.Acks {
	case "0":
		return kgo.NoAck()
	case "1":
		return kgo.LeaderAck()
	default: // "all" or empty → safest default
		return kgo.AllISRAcks()
	}
}

// compressionCodec returns the kgo compression codec for this topic.
func (t *ProducerTopicConfig) compressionCodec() []kgo.CompressionCodec {
	switch t.Compression {
	case "gzip":
		return []kgo.CompressionCodec{kgo.GzipCompression()}
	case "snappy":
		return []kgo.CompressionCodec{kgo.SnappyCompression()}
	case "lz4":
		return []kgo.CompressionCodec{kgo.Lz4Compression()}
	case "zstd":
		return []kgo.CompressionCodec{kgo.ZstdCompression()}
	default:
		return []kgo.CompressionCodec{kgo.NoCompression()}
	}
}

// batchMaxBytes returns MaxMessageBytes with a 1MB default.
func (t *ProducerTopicConfig) batchMaxBytes() int32 {
	if t.MaxMessageBytes > 0 {
		return t.MaxMessageBytes
	}
	return 1024 * 1024 // 1MB
}

// lingerDuration returns the linger as a Duration with a 5ms default.
func (t *ProducerTopicConfig) lingerDuration() time.Duration {
	if t.LingerMs > 0 {
		return time.Duration(t.LingerMs) * time.Millisecond
	}
	return 5 * time.Millisecond
}

// retryBackoff returns an exponential backoff function capped at 3s.
func (t *ProducerTopicConfig) retryBackoff() func(int) time.Duration {
	backoffMs := t.RetryBackoffMs
	if backoffMs <= 0 {
		backoffMs = 100
	}
	return func(tries int) time.Duration {
		d := time.Duration(tries*backoffMs) * time.Millisecond
		if d > 3*time.Second {
			return 3 * time.Second
		}
		return d
	}
}

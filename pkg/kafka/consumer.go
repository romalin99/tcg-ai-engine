package kafka

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"tcg-ai-engine/pkg/gos"
	"tcg-ai-engine/pkg/logs"
	"tcg-ai-engine/pkg/metrics"
)

// kgoLogger adapts franz-go's kgo.Logger to the project logs package, so Kafka client
// diagnostics flow through the same structured + rotated pipeline as the rest of the
// service. Pointing kgo's plain-text BasicLogger at the log file's descriptor directly
// would interleave unstructured lines into the encoded (JSON) log stream.
type kgoLogger struct{}

// Level gates which kgo messages are emitted — warn/error only, matching the previous
// BasicLogger(os.Stderr, LogLevelWarn) behaviour.
func (kgoLogger) Level() kgo.LogLevel { return kgo.LogLevelWarn }

// Log maps a kgo log event onto the project logger, rendering kgo's alternating
// key/value pairs as a "k=v" suffix. The assembled text is passed as the message with
// no printf args, so values containing '%' are never reinterpreted.
func (kgoLogger) Log(level kgo.LogLevel, msg string, keyvals ...any) {
	ctx := context.Background()
	if len(keyvals) > 0 {
		msg += " " + kgoKeyvals(keyvals)
	}
	switch level {
	case kgo.LogLevelError:
		logs.Err(ctx, msg)
	case kgo.LogLevelWarn:
		logs.Warn(ctx, msg)
	case kgo.LogLevelInfo:
		logs.Info(ctx, msg)
	default: // Debug / None
		logs.Debug(ctx, msg)
	}
}

// kgoKeyvals renders kgo's alternating key/value pairs as "k=v k=v".
func kgoKeyvals(keyvals []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(keyvals); i += 2 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%v=%v", keyvals[i], keyvals[i+1])
	}
	if len(keyvals)%2 == 1 { // dangling key with no value
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%v=?", keyvals[len(keyvals)-1])
	}
	return b.String()
}

// icount tracks how many consumer poll loops have exited; used only for diagnostics.
var icount atomic.Uint64

// Consumer manages one or more Kafka consumer groups and their lifecycle.
type Consumer struct {
	config       *ConsumerConfig
	wg           *sync.WaitGroup
	consumerGrps map[string]*ConsumerGrp
	cancelFuncs  []context.CancelFunc
	mu           sync.RWMutex
}

// topicPartition identifies a partition within a topic (commit batches are keyed by it).
type topicPartition struct {
	topic     string
	partition int32
}

// commitTask is one batch of records to commit. When sync != nil the commit worker
// closes it after draining everything ahead of it — used by onPartitionsRevoked to
// flush pending commits synchronously before a partition is taken away.
type commitTask struct {
	sync    chan struct{}
	key     topicPartition
	records []*kgo.Record
}

// ConsumerGrp drives a single kgo.Client (one topic, one group) using MANUAL batch
// commit: records are handled by a bounded per-partition worker pool, then their
// offsets are committed in batches by a dedicated commit worker (with retry + metrics).
// Auto-commit is disabled; a cooperative-sticky balancer + BlockRebalanceOnPoll +
// revoke-flush minimise duplicate delivery across rebalances.
type ConsumerGrp struct {
	ctx             context.Context
	cancel          context.CancelFunc
	client          *kgo.Client
	handler         func(record *kgo.Record)
	sem             chan struct{}
	commitCh        chan commitTask
	group           string
	topic           string
	commitWg        sync.WaitGroup
	procWg          sync.WaitGroup
	commitMaxRetry  int
	commitTimeout   time.Duration
	commitBlockWarn time.Duration
	revokeFlushTO   time.Duration
	shutdownTO      time.Duration
	fetchErrBackoff time.Duration
	commitChanCap   int
	commitBatchSize int
	maxPollRecords  int
	maxWorkers      int
	closing         atomic.Bool
}

// SetSafeHandler sets the message handler function for this consumer group.
func (cg *ConsumerGrp) SetSafeHandler(handler func(record *kgo.Record)) {
	cg.handler = handler
}

// safeHandler invokes the user handler inside a recover block so a panic in the
// handler does not crash the worker goroutine.
func (cg *ConsumerGrp) safeHandler(record *kgo.Record) {
	defer func() {
		if rec := recover(); rec != nil {
			logs.Err(context.Background(),
				"panic in handler: %v topic=%s partition=%d offset=%d",
				rec, record.Topic, record.Partition, record.Offset)
		}
	}()
	if cg.handler != nil {
		cg.handler(record)
	}
}

// start launches the commit worker and the poll loop. The poll loop is tracked by
// the Consumer-level WaitGroup so Close() can wait for a graceful drain.
func (cg *ConsumerGrp) start(wg *sync.WaitGroup) {
	cg.commitWg.Add(1)
	gos.GoSafe(cg.commitWorker) // commitWorker defers commitWg.Done()

	wg.Add(1)
	gos.GoSafe(func() {
		defer wg.Done()
		defer cg.client.CloseAllowingRebalance()
		cg.run()
		logs.Info(context.Background(), "topic %s consumer stopped: %v, icount=%v",
			cg.topic, cg.ctx.Err(), icount.Add(1))
	})
}

// run is the poll-process-commit loop; it blocks until ctx is cancelled, then drains.
func (cg *ConsumerGrp) run() {
	logs.Info(cg.ctx, "consumer started: topic=%s group=%s maxWorkers=%d commitBatch=%d",
		cg.topic, cg.group, cg.maxWorkers, cg.commitBatchSize)

	for {
		select {
		case <-cg.ctx.Done():
			cg.shutdown()
			return
		default:
		}

		fetches := cg.client.PollRecords(cg.ctx, cg.maxPollRecords)
		if err := fetches.Err0(); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, kgo.ErrClientClosed) {
				cg.shutdown()
				return
			}
			logs.Err(cg.ctx, "poll error topic=%s: %v", cg.topic, err)
			cg.client.AllowRebalance()
			cg.backoffOnFetchError()
			continue
		}

		if cg.checkFetchErrors(fetches) {
			cg.client.AllowRebalance()
			cg.backoffOnFetchError()
			continue
		}

		records := fetches.Records()
		if len(records) == 0 {
			cg.client.AllowRebalance()
			continue
		}

		cg.processFetchedRecords(records)

		// AllowRebalance may invoke onPartitionsRevoked, which flushes commitCh.
		cg.client.AllowRebalance()
	}
}

// backoffOnFetchError performs one ctx-interruptible pause after a fetch error so the
// loop does not hot-spin while a broker is degraded. Must run AFTER AllowRebalance().
func (cg *ConsumerGrp) backoffOnFetchError() {
	if cg.fetchErrBackoff <= 0 {
		return
	}
	timer := time.NewTimer(cg.fetchErrBackoff)
	defer timer.Stop()
	select {
	case <-cg.ctx.Done():
	case <-timer.C:
	}
}

// checkFetchErrors logs partition-level fetch errors and reports whether any occurred.
func (cg *ConsumerGrp) checkFetchErrors(fetches kgo.Fetches) bool {
	had := false
	fetches.EachError(func(topic string, partition int32, err error) {
		had = true
		logs.Err(cg.ctx, "fetch error topic=%s partition=%d: %v", topic, partition, err)
	})
	return had
}

// processFetchedRecords processes each partition's records concurrently (bounded by
// the worker semaphore) and waits for the whole batch before returning, so the
// subsequent AllowRebalance sees all commits enqueued.
func (cg *ConsumerGrp) processFetchedRecords(records []*kgo.Record) {
	batches := groupByTopicPartition(records)

	var wg sync.WaitGroup
	for key, rs := range batches {
		wg.Add(1)
		cg.procWg.Add(1) // before the goroutine, so shutdown's Wait can't race Add
		go func(key topicPartition, rs []*kgo.Record) {
			defer wg.Done()
			defer cg.procWg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					logs.Err(context.Background(), "panic in partition worker: %v topic=%s partition=%d",
						rec, key.topic, key.partition)
				}
			}()

			cg.sem <- struct{}{}
			defer func() { <-cg.sem }()

			cg.processPartition(key, rs)
		}(key, rs)
	}

	wg.Wait()
}

// processPartition runs the handler over a partition's records in order and commits
// them in batches of commitBatchSize. The handler owns its own error handling; every
// record the handler returns from is treated as processed (at-least-once).
func (cg *ConsumerGrp) processPartition(key topicPartition, records []*kgo.Record) {
	processed := make([]*kgo.Record, 0, cg.commitBatchSize)

	for i, r := range records {
		if cg.ctx.Err() != nil {
			break // stop early on shutdown; leftover committed by the safety flush below
		}

		cg.safeHandler(r)
		processed = append(processed, r)

		if len(processed) >= cg.commitBatchSize || i == len(records)-1 {
			if len(processed) > 0 {
				cg.enqueueCommit(cg.ctx, key, processed)
				processed = processed[:0]
			}
		}
	}

	// Safety flush for records left when the loop broke early on ctx cancel. Uses an
	// independent context so the enqueue can still succeed during graceful shutdown
	// (the commit worker is still running until shutdown closes commitCh).
	if len(processed) > 0 {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), cg.commitBlockWarn)
		cg.enqueueCommit(flushCtx, key, processed)
		flushCancel()
	}
}

// enqueueCommit copies records into an isolated slice and hands them to the commit
// worker. A persistently blocked channel is logged once (broker degraded); ctx
// cancellation or a closing consumer aborts the enqueue (records reprocess on restart).
func (cg *ConsumerGrp) enqueueCommit(ctx context.Context, key topicPartition, records []*kgo.Record) {
	if len(records) == 0 || cg.closing.Load() {
		return
	}

	batch := make([]*kgo.Record, len(records))
	copy(batch, records)
	task := commitTask{key: key, records: batch}

	warnTimer := time.NewTimer(cg.commitBlockWarn)
	defer warnTimer.Stop()

	select {
	case cg.commitCh <- task:
	case <-ctx.Done():
		logs.Warn(cg.ctx, "ctx cancelled before commit enqueue; topic=%s partition=%d count=%d (will reprocess)",
			key.topic, key.partition, len(batch))
	case <-warnTimer.C:
		logs.Warn(cg.ctx, "commitCh blocked; broker may be degraded topic=%s len=%d cap=%d",
			key.topic, len(cg.commitCh), cap(cg.commitCh))
		select {
		case cg.commitCh <- task:
		case <-ctx.Done():
			logs.Warn(cg.ctx, "ctx cancelled after commitCh warn; topic=%s partition=%d count=%d (will reprocess)",
				key.topic, key.partition, len(batch))
		}
	}
}

// commitWorker serially commits batches from commitCh and exits when it is closed.
// A task with sync != nil is a flush barrier (see onPartitionsRevoked).
func (cg *ConsumerGrp) commitWorker() {
	defer cg.commitWg.Done()

	for task := range cg.commitCh {
		if task.sync != nil {
			close(task.sync)
			continue
		}
		cg.commitWithRetry(task.key, task.records)
	}

	logs.Info(context.Background(), "commit worker exited: topic=%s", cg.topic)
}

// commitWithRetry commits one batch with bounded exponential backoff + jitter. The
// commit itself uses a fresh context.Background() with commitTimeout so it still runs
// during shutdown drain; only the inter-retry wait honours ctx cancellation.
func (cg *ConsumerGrp) commitWithRetry(key topicPartition, records []*kgo.Record) {
	if len(records) == 0 {
		return
	}

	backoff := 100 * time.Millisecond
	for attempt := 0; attempt <= cg.commitMaxRetry; attempt++ {
		commitCtx, cancel := context.WithTimeout(context.Background(), cg.commitTimeout)
		err := cg.client.CommitRecords(commitCtx, records...)
		cancel()
		if err == nil {
			cg.metricCommitSuccess(key.topic)
			logs.Info(context.Background(), "committed topic=%s, partition=%d, batch=%d, lastOffset=%d",
				key.topic, key.partition, len(records), records[len(records)-1].Offset)
			return
		}

		if attempt == cg.commitMaxRetry {
			cg.metricCommitFailure(key.topic)
			logs.Err(context.Background(), "MANUAL INTERVENTION: final commit failed topic=%s partition=%d count=%d err=%v",
				key.topic, key.partition, len(records), err)
			return
		}

		cg.metricCommitRetry(key.topic)
		logs.Warn(context.Background(), "commit failed, retrying attempt=%d/%d topic=%s partition=%d err=%v",
			attempt+1, cg.commitMaxRetry, key.topic, key.partition, err)

		jitter := time.Duration(rand.Int63n(int64(backoff)/2 + 1)) //nolint:gosec // non-crypto jitter
		retryTimer := time.NewTimer(backoff + jitter)
		select {
		case <-cg.ctx.Done():
			retryTimer.Stop()
			return
		case <-retryTimer.C:
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// metricCommitSuccess / Retry / Failure update the Kafka commit counters when the
// metrics subsystem has been initialised (guarded so they are no-ops otherwise).
func (cg *ConsumerGrp) metricCommitSuccess(topic string) {
	if metrics.KafkaMetricsEnabled() {
		metrics.KafkaCommitSuccessTotal.WithLabelValues(cg.group, topic).Inc()
		metrics.KafkaCommitConsecutiveFailures.WithLabelValues(cg.group, topic).Set(0)
	}
}

func (cg *ConsumerGrp) metricCommitRetry(topic string) {
	if metrics.KafkaMetricsEnabled() {
		metrics.KafkaCommitRetriesTotal.WithLabelValues(cg.group, topic).Inc()
	}
}

func (cg *ConsumerGrp) metricCommitFailure(topic string) {
	if metrics.KafkaMetricsEnabled() {
		metrics.KafkaCommitFailuresTotal.WithLabelValues(cg.group, topic).Inc()
		metrics.KafkaCommitConsecutiveFailures.WithLabelValues(cg.group, topic).Inc()
	}
}

// onPartitionsRevoked runs inside AllowRebalance(). Because of BlockRebalanceOnPoll,
// processing of the current batch is finished and all commits are enqueued. It sends a
// sync barrier and waits (bounded by revokeFlushTO) for the commit worker to drain so
// revoked partitions' offsets are persisted before reassignment — minimising re-delivery.
func (cg *ConsumerGrp) onPartitionsRevoked(ctx context.Context, _ *kgo.Client, revoked map[string][]int32) {
	logs.Info(ctx, "partitions revoked topic=%s: %v", cg.topic, revoked)

	if cg.closing.Load() {
		return // shutdown already drives the drain
	}

	done := make(chan struct{})
	select {
	case cg.commitCh <- commitTask{sync: done}:
	case <-ctx.Done():
		logs.Warn(ctx, "revoke: ctx expired before flush enqueue topic=%s", cg.topic)
		return
	}

	flushTimer := time.NewTimer(cg.revokeFlushTO)
	defer flushTimer.Stop()
	select {
	case <-done:
		logs.Info(ctx, "revoke: commit flush complete topic=%s", cg.topic)
	case <-flushTimer.C:
		logs.Warn(ctx, "revoke: flush timed out topic=%s timeout=%v", cg.topic, cg.revokeFlushTO)
	case <-ctx.Done():
		logs.Warn(ctx, "revoke: ctx cancelled during flush topic=%s", cg.topic)
	}
}

func (cg *ConsumerGrp) onPartitionsAssigned(ctx context.Context, _ *kgo.Client, assigned map[string][]int32) {
	logs.Info(ctx, "partitions assigned topic=%s: %v", cg.topic, assigned)
}

// shutdown drains gracefully: wait for in-flight processing, close commitCh, then wait
// (bounded by shutdownTO) for the commit worker to finish the remaining batches.
func (cg *ConsumerGrp) shutdown() {
	logs.Info(context.Background(), "shutdown: waiting for in-flight processing topic=%s", cg.topic)
	cg.procWg.Wait()

	logs.Info(context.Background(), "shutdown: draining commit channel topic=%s", cg.topic)
	cg.closing.Store(true)
	close(cg.commitCh)

	done := make(chan struct{})
	go func() {
		cg.commitWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logs.Info(context.Background(), "consumer exited gracefully topic=%s", cg.topic)
	case <-time.After(cg.shutdownTO):
		logs.Warn(context.Background(), "shutdown timeout: some commits may not have persisted topic=%s", cg.topic)
	}
}

// NewConsumer creates a new Consumer from the provided ConsumerConfig.
// It returns nil and calls logs.Fatal if cfg is nil or contains no brokers.
func NewConsumer(cfg *ConsumerConfig) *Consumer {
	if cfg == nil || len(cfg.Brokers) == 0 {
		logs.Fatal(context.Background(), "Kafka consumer config is invalid")
		return nil
	}

	instance := &Consumer{
		config:       cfg,
		wg:           &sync.WaitGroup{},
		consumerGrps: make(map[string]*ConsumerGrp),
	}

	logs.Info(context.Background(), "Kafka consumer created: brokers=%v clientID=%s",
		cfg.Brokers, cfg.ClientID)

	return instance
}

// SubscribeTopic subscribes topicName to the configured consumer group. All instances
// across all hosts join ONE Kafka group (group_id verbatim), so Kafka balances the
// topic's partitions among them and each message is delivered to exactly one instance.
// Equivalent to SubscribeTopicSharedGroup (per-instance / broadcast mode was removed).
func (c *Consumer) SubscribeTopic(topicName string, handler func(record *kgo.Record)) error {
	return c.subscribe(topicName, handler)
}

// SubscribeTopicSharedGroup subscribes using the configured group_id verbatim
// (NO hostname suffix). All instances across all hosts join ONE Kafka consumer
// group, so Kafka balances the topic's partitions among them and each message is
// delivered to exactly one instance — no cross-instance duplication. A stable
// group.id also lets offsets resume across restarts regardless of the (possibly
// random, e.g. k8s) hostname.
//
// Use this for accumulation / work-queue topics such as PLAYER_BEAT_TIMELIMIT_USAGE.
func (c *Consumer) SubscribeTopicSharedGroup(topicName string, handler func(record *kgo.Record)) error {
	return c.subscribe(topicName, handler)
}

// subscribe builds a dedicated manual-commit kgo.Client for topicName and starts its
// poll loop. All instances join the configured group_id verbatim (one shared group
// across hosts): Kafka balances the topic's partitions among the instances and each
// message is delivered to exactly one of them.
func (c *Consumer) subscribe(topicName string, handler func(record *kgo.Record)) error {
	lower := strings.ToLower(topicName)
	tc := c.config.EffectiveTopicConsumerConfig(lower)

	groupID := tc.GroupID

	// Cap worker count to the topic's partition count (one goroutine per partition is
	// the useful maximum); fall back to the configured max_workers on metadata error.
	// Done before locking so the metadata round-trip does not hold c.mu.
	workers := intOr(tc.MaxWorkers, 1)
	if pc, err := fetchTopicPartitionCount(context.Background(), c.config.Brokers, topicName, c.config.ClientID+"-metadata"); err != nil {
		logs.Warn(context.Background(), "partition count query failed topic=%s; using configured max_workers=%d err=%v",
			topicName, workers, err)
	} else if pc > 0 {
		workers = pc + 1
		logs.Info(context.Background(), "max_workers set to partition count topic=%s partitions=%d, prev=%d",
			topicName, pc, workers)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.consumerGrps[lower]; exists {
		logs.Warn(context.Background(), "topic %s already subscribed", topicName)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cg := &ConsumerGrp{
		ctx:             ctx,
		cancel:          cancel,
		handler:         handler,
		group:           groupID,
		topic:           topicName,
		maxWorkers:      workers,
		maxPollRecords:  c.config.MaxPollRecordsOrDefault(),
		commitBatchSize: intOr(tc.CommitBatchSize, 500),
		commitChanCap:   intOr(tc.CommitChanCap, 500),
		commitMaxRetry:  intOr(tc.CommitMaxRetry, 3),
		commitTimeout:   msToDur(tc.CommitTimeoutMs, 10*time.Second),
		commitBlockWarn: msToDur(tc.CommitBlockWarnMs, 5*time.Second),
		revokeFlushTO:   msToDur(tc.RevokeFlushTimeoutMs, 8*time.Second),
		shutdownTO:      msToDur(tc.ShutdownTimeoutMs, 30*time.Second),
		fetchErrBackoff: msToDur(tc.FetchErrorBackoffMs, time.Second),
	}
	cg.commitCh = make(chan commitTask, cg.commitChanCap)
	cg.sem = make(chan struct{}, cg.maxWorkers)

	client, err := kgo.NewClient(c.buildConsumerOpts(tc, topicName, groupID, cg)...)
	if err != nil {
		cancel()
		return fmt.Errorf("create Kafka client for topic %s: %w", topicName, err)
	}
	cg.client = client

	c.chainCancel(cancel)
	cg.start(c.wg)
	c.consumerGrps[lower] = cg

	logs.Info(context.Background(),
		"Kafka consumer subscribed: topic=%s, group=%s, maxWorkers=%d, offsetReset=%s, commitBatch=%d",
		topicName, groupID, cg.maxWorkers, tc.AutoOffsetReset, cg.commitBatchSize)

	return nil
}

// buildConsumerOpts builds the kgo.Opt slice for a manual-commit consumer group.
func (c *Consumer) buildConsumerOpts(tc ConsumerTopicConfig, topicName, groupID string, cg *ConsumerGrp) []kgo.Opt {
	return []kgo.Opt{
		// broker / identity
		kgo.SeedBrokers(c.config.Brokers...),
		kgo.ClientID(tc.ClientID),
		kgo.WithLogger(kgoLogger{}),

		// group / topic
		kgo.ConsumerGroup(groupID),
		kgo.ConsumeTopics(topicName),
		kgo.ConsumeResetOffset(offsetResetToKgo(tc.AutoOffsetReset)),

		// manual commit + cooperative rebalancing
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.Balancers(kgo.CooperativeStickyBalancer()),
		kgo.OnPartitionsRevoked(cg.onPartitionsRevoked),
		kgo.OnPartitionsAssigned(cg.onPartitionsAssigned),

		// session / rebalance
		kgo.SessionTimeout(msToDur(tc.SessionTimeoutMs, 30*time.Second)),
		kgo.HeartbeatInterval(msToDur(tc.HeartbeatIntervalMs, 3*time.Second)),
		kgo.RebalanceTimeout(msToDur(tc.RebalanceTimeoutMs, 60*time.Second)),

		// fetch tuning
		kgo.FetchMinBytes(int32Or(tc.FetchMinBytes, 1)),
		kgo.FetchMaxWait(tc.fetchMaxWait()),
		kgo.FetchMaxBytes(int32Or(tc.FetchMaxBytes, 10<<20)),
		kgo.FetchMaxPartitionBytes(int32Or(tc.FetchMaxPartitionBytes, 10<<20)),
		kgo.MaxConcurrentFetches(intOr(tc.MaxConcurrentFetches, 8)),
	}
}

// offsetResetToKgo converts "earliest" to AtStart and everything else to AtEnd.
func offsetResetToKgo(autoOffsetReset string) kgo.Offset {
	if autoOffsetReset == "earliest" {
		return kgo.NewOffset().AtStart()
	}
	return kgo.NewOffset().AtEnd()
}

// fetchMaxWait returns FetchMaxWaitMs as a Duration with a 500ms default.
func (t *ConsumerTopicConfig) fetchMaxWait() time.Duration {
	if t.FetchMaxWaitMs > 0 {
		return time.Duration(t.FetchMaxWaitMs) * time.Millisecond
	}
	return 500 * time.Millisecond
}

// chainCancel appends cancel to the consumer's list of active cancel functions, so
// Close() can cancel every subscription in O(N).
func (c *Consumer) chainCancel(cancel context.CancelFunc) {
	c.cancelFuncs = append(c.cancelFuncs, cancel)
}

// NewConsumerGrp creates a Consumer using a minimal configuration.
//
// Deprecated: Use NewConsumer with a ConsumerConfig instead.
// This function is retained for backward compatibility only.
func NewConsumerGrp(kafkaAddr []string, groupId string) *Consumer {
	ctx := context.Background()
	logs.Info(ctx, "Kafka init addr = %v", kafkaAddr)

	instance := &Consumer{
		wg:           &sync.WaitGroup{},
		consumerGrps: make(map[string]*ConsumerGrp),
	}

	//nolint:gosec // cancel is stored and called in Close()
	ctxC, cancel := context.WithCancel(context.Background())
	instance.cancelFuncs = append(instance.cancelFuncs, cancel)

	instance.consumerGrps["default"] = &ConsumerGrp{
		ctx:        ctxC,
		cancel:     cancel,
		group:      groupId,
		maxWorkers: 1,
	}

	logs.Info(ctx, "Kafka init successfully")
	return instance
}

// SetConsumerGrp sets the message handler on the default consumer group.
//
// Deprecated: Use SubscribeTopic with an explicit handler instead.
// This method is retained for backward compatibility with NewConsumerGrp.
func (c *Consumer) SetConsumerGrp(handler func(record *kgo.Record)) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if grp, exists := c.consumerGrps["default"]; exists {
		grp.SetSafeHandler(handler)
	}
}

// Close cancels all active consumer group contexts and waits for the poll loops to
// drain and commit. Each ConsumerGrp closes its own kgo.Client on exit.
func (c *Consumer) Close() error {
	for _, cancel := range c.cancelFuncs {
		cancel()
	}
	c.wg.Wait()
	logs.Info(context.Background(), "kafka consumer closed")
	return nil
}

// ---------------------------------------------------------------
// helpers
// ---------------------------------------------------------------

// groupByTopicPartition buckets records by their (topic, partition) so each partition
// can be processed in order by its own worker.
func groupByTopicPartition(records []*kgo.Record) map[topicPartition][]*kgo.Record {
	batches := make(map[topicPartition][]*kgo.Record, 16)
	for _, r := range records {
		key := topicPartition{topic: r.Topic, partition: r.Partition}
		batches[key] = append(batches[key], r)
	}
	return batches
}

// fetchTopicPartitionCount queries the partition count for topic using a short-lived
// metadata-only client. Returns an error on failure; callers decide the fallback.
func fetchTopicPartitionCount(ctx context.Context, brokers []string, topic, clientID string) (int, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID(clientID))
	if err != nil {
		return 0, fmt.Errorf("create metadata client: %w", err)
	}
	defer cl.Close()

	mdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	adm := kadm.NewClient(cl)
	topicDetails, err := adm.ListTopics(mdCtx, topic)
	if err != nil {
		return 0, fmt.Errorf("list topics: %w", err)
	}
	td, ok := topicDetails[topic]
	if !ok {
		return 0, fmt.Errorf("topic %q not found in metadata response", topic)
	}
	if td.Err != nil {
		return 0, fmt.Errorf("topic %q metadata error: %w", topic, td.Err)
	}
	count := len(td.Partitions)
	if count == 0 {
		return 0, fmt.Errorf("topic %q has 0 partitions", topic)
	}
	return count, nil
}

// msToDur converts a millisecond config value to a Duration, falling back to def when <= 0.
func msToDur(ms int, def time.Duration) time.Duration {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return def
}

// intOr returns v when positive, else def.
func intOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// int32Or returns v when positive, else def.
func int32Or(v, def int32) int32 {
	if v > 0 {
		return v
	}
	return def
}

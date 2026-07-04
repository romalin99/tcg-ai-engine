package redis

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"tcg-ai-engine/pkg/gos"
	"tcg-ai-engine/pkg/logs"
	"tcg-ai-engine/pkg/metrics"
)

type RedisMonitor struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type Config struct {
	MapRedisMonitor map[string]*RedisMonitor
	MasterName      string   `mapstructure:"master_name"`
	Password        string   `mapstructure:"password"`
	Addr            []string `mapstructure:"addr"`
	Dbs             []struct {
		Db                   int `mapstructure:"db"`
		PoolSize             int `mapstructure:"poolSize"`
		SetDefaultExpiration int `mapstructure:"setDefaultExpiration"`
	} `mapstructure:"dbs"`
	Db        int `mapstructure:"db"`
	closeOnce sync.Once
}

var rdb *redis.Client

// key format: dbidx:0, dbidx:1, dbidx:2
var rdbMap map[string]*redis.Client

type RedisInstance struct {
	Rdb *redis.Client
	TTL time.Duration
}

// key format: dbidx:0, dbidx:1, dbidx:2
var rdbMapWithTtl map[string]*RedisInstance

// newClientOptions returns the standard redis.Options for a single-node client.
func (r *Config) newClientOptions(db, poolSize int) *redis.Options {
	return &redis.Options{
		Addr:            r.Addr[0],
		Password:        r.Password,
		DB:              db,
		DialTimeout:     10 * time.Second,
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		PoolSize:        poolSize,
		MinIdleConns:    10,
		PoolTimeout:     10 * time.Second,
		ConnMaxIdleTime: 30 * time.Minute,
		MaxRetries:      3,
		ConnMaxLifetime: 1 * time.Hour,
	}
}

// newFailoverOptions returns redis.FailoverOptions for a sentinel-based client.
func (r *Config) newFailoverOptions(db, poolSize int) *redis.FailoverOptions {
	return &redis.FailoverOptions{
		MasterName:      r.MasterName,
		SentinelAddrs:   r.Addr,
		Password:        r.Password,
		DB:              db,
		DialTimeout:     10 * time.Second,
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		PoolSize:        poolSize,
		MinIdleConns:    10,
		PoolTimeout:     10 * time.Second,
		ConnMaxIdleTime: 30 * time.Minute,
		MaxRetries:      3,
		ConnMaxLifetime: 1 * time.Hour,
	}
}

// startMonitor launches a pool stats monitor goroutine and returns the monitor handle.
func (r *Config) startMonitor(desc string, client *redis.Client, poolSize int) *RedisMonitor {
	//nolint:gosec
	ctx, cancel := context.WithCancel(context.Background())
	m := &RedisMonitor{ctx: ctx, cancel: cancel}
	m.wg.Add(1)
	go r.monitorRedisPool(ctx, client, poolSize, &m.wg, desc)
	return m
}

func (r *Config) Init() *redis.Client {
	rdb = redis.NewClient(r.newClientOptions(r.Db, 1000))
	if _, err := rdb.Ping(context.Background()).Result(); err != nil {
		logs.Fatalf(context.Background(), "redis Init ping failed (db=%d): %v", r.Db, err)
	}
	return rdb
}

func (r *Config) InitDBS() map[string]*redis.Client {
	rdbMap = make(map[string]*redis.Client, len(r.Dbs))
	for _, item := range r.Dbs {
		c := redis.NewClient(r.newClientOptions(item.Db, 1000))
		if _, err := c.Ping(context.Background()).Result(); err != nil {
			logs.Fatalf(context.Background(), "redis InitDBS ping failed (db=%d): %v", item.Db, err)
		}
		rdbMap[fmt.Sprintf("dbidx:%d", item.Db)] = c
	}
	return rdbMap
}

func GetRdbMap() map[string]*redis.Client {
	return rdbMap
}

func (r *Config) monitorRedisPool(ctx context.Context, client *redis.Client, maxPoolSize int, wg *sync.WaitGroup, desc string) {
	defer wg.Done()
	defer gos.Recover()

	lastMinute := -1
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var last redis.PoolStats
	metrics.RedisMaxPoolSize.WithLabelValues(desc).Set(float64(maxPoolSize))

	for {
		select {
		case <-ticker.C:
			stats := client.PoolStats()
			totalConns := int(stats.TotalConns)
			idleConns := int(stats.IdleConns)
			active := totalConns - idleConns
			useRate := float64(active) / float64(maxPoolSize) * 100

			if useRate > 80 {
				logs.Warn(ctx, "[%v] reach warning level(80%%), active: %d, maxPoolSize: %d", desc, active, maxPoolSize)
			}
			if stats.Timeouts > 0 {
				logs.Warn(ctx, "[%v] maxPoolSize=%v, timeouts occurred: %d", desc, maxPoolSize, stats.Timeouts)
			}

			now := time.Now()
			if now.Minute()%5 == 0 && now.Minute() != lastMinute {
				logs.Warn(ctx, "[%v] maxPoolSize=%v, stats=%+v", desc, maxPoolSize, stats)
				lastMinute = now.Minute()
			}

			metrics.RedisHits.WithLabelValues(desc).Add(float64(stats.Hits - last.Hits))
			metrics.RedisMisses.WithLabelValues(desc).Add(float64(stats.Misses - last.Misses))
			metrics.RedisTimeouts.WithLabelValues(desc).Add(float64(stats.Timeouts - last.Timeouts))
			metrics.RedisWaitCount.WithLabelValues(desc).Add(float64(stats.WaitCount - last.WaitCount))
			metrics.RedisStaleConns.WithLabelValues(desc).Add(float64(stats.StaleConns))
			metrics.RedisTotalConns.WithLabelValues(desc).Set(float64(stats.TotalConns))
			metrics.RedisIdleConns.WithLabelValues(desc).Set(float64(stats.IdleConns))
			metrics.RedisWaitDurationNs.WithLabelValues(desc).Set(float64(stats.WaitDurationNs-last.WaitDurationNs) / 1e9)
			last = *stats

		case <-ctx.Done():
			logs.Info(ctx, "Stopping Redis pool monitor...")
			return
		}
	}
}

func GetDbInstance(idx int32) (*RedisInstance, error) {
	key := "dbidx:" + strconv.FormatInt(int64(idx), 10)
	inst, ok := rdbMapWithTtl[key]
	if !ok || inst == nil || inst.Rdb == nil {
		err := fmt.Errorf("redis instance not found or invalid for key=%s (dbidx:%d)", key, idx)
		logs.Err(context.Background(), "Get redis instance err=%v", err)
		return nil, err
	}
	return inst, nil
}

// InitDBSV2 initializes single-node Redis DB instances.
// The key format is: dbidx:0, dbidx:1, dbidx:2.
func (r *Config) InitDBSV2() map[string]*RedisInstance {
	ctx := context.Background()
	rdbMapWithTtl = make(map[string]*RedisInstance, len(r.Dbs))
	r.MapRedisMonitor = make(map[string]*RedisMonitor, len(r.Dbs))

	for _, item := range r.Dbs {
		c := redis.NewClient(r.newClientOptions(item.Db, item.PoolSize))
		pong, err := c.Ping(context.Background()).Result()
		if err != nil {
			logs.Fatalf(ctx, "redis InitDBSV2 ping failed (db=%d): %v", item.Db, err)
		}
		logs.Info(ctx, "dbidx %v, pong: %s", item.Db, pong)

		key := fmt.Sprintf("dbidx:%d", item.Db)
		rdbMapWithTtl[key] = &RedisInstance{Rdb: c, TTL: time.Duration(item.SetDefaultExpiration) * time.Second}
		r.MapRedisMonitor[key] = r.startMonitor(fmt.Sprintf("redis-dbidx:%d-pool", item.Db), c, item.PoolSize)
	}

	logs.Info(ctx, "Redis %v init successfully", r.Addr)
	return rdbMapWithTtl
}

// InitSentinelDBS initializes sentinel Redis DB instances.
// The key format is: dbidx:0, dbidx:1, dbidx:2.
func (r *Config) InitSentinelDBS() map[string]*RedisInstance {
	ctx := context.Background()
	rdbMapWithTtl = make(map[string]*RedisInstance, len(r.Dbs))
	r.MapRedisMonitor = make(map[string]*RedisMonitor, len(r.Dbs))

	for _, item := range r.Dbs {
		c := redis.NewFailoverClient(r.newFailoverOptions(item.Db, item.PoolSize))
		pong, err := c.Ping(context.Background()).Result()
		if err != nil {
			logs.Fatalf(ctx, "redis InitSentinelDBS ping failed (db=%d): %v", item.Db, err)
		}
		logs.Info(ctx, "dbidx %v, pong: %s", item.Db, pong)

		key := fmt.Sprintf("dbidx:%d", item.Db)
		rdbMapWithTtl[key] = &RedisInstance{Rdb: c, TTL: time.Duration(item.SetDefaultExpiration) * time.Second}
		r.MapRedisMonitor[key] = r.startMonitor(fmt.Sprintf("redis-dbidx:%d-pool", item.Db), c, item.PoolSize)
	}

	return rdbMapWithTtl
}

func GetRdbMapV2() map[string]*RedisInstance {
	return rdbMapWithTtl
}

// Close is idempotent: the first call stops all monitor goroutines (waiting for
// each to exit) and then closes every Redis connection. Subsequent calls are
// no-ops, which means both ComManager.Close() and cfg.Close() can safely call
// this method without double-closing connections or racing on monitor state.
func (r *Config) Close() {
	r.closeOnce.Do(func() {
		// 1. Cancel every monitor goroutine and wait for it to exit before
		//    touching the underlying connections.
		for _, m := range r.MapRedisMonitor {
			if m != nil {
				m.cancel()
				m.wg.Wait()
			}
		}

		// 2. Close client connections now that no goroutine is reading from them.
		for _, inst := range rdbMapWithTtl {
			if inst != nil && inst.Rdb != nil {
				_ = inst.Rdb.Close()
			}
		}
		if rdb != nil {
			_ = rdb.Close()
		}

		// 3. Nil out package-level globals so any use-after-close access is
		//    caught early (returns "redis: client is closed" rather than silently
		//    operating on a stale pointer).
		rdbMapWithTtl = nil
		rdb = nil
	})
}

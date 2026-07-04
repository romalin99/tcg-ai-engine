package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/godror/godror"
	"github.com/jmoiron/sqlx"

	"tcg-ai-engine/pkg/gos"
	"tcg-ai-engine/pkg/logs"
	"tcg-ai-engine/pkg/metrics"
)

type OracleDBMonitor struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type Config struct {
	MapOracleDBMonitor map[string]*OracleDBMonitor
	OraclePWD          string `mapstructure:"passwd"`
	OracleConnectStr   string `mapstructure:"addr_connect_stringer"`
	AppendedOptions    string `mapstructure:"appendedOptions"`
	EnvVar             string `mapstructure:"env"`
	OracleUser         string `mapstructure:"user"`
	MaxIdleConn        int    `mapstructure:"max_idle_conn"`
	MaxIdleTime        int    `mapstructure:"max_idle_time"`
	StatsInterval      int    `mapstructure:"stats_interval"`
	ReadTimeOut        int    `mapstructure:"read_timeout"`
	WriteTimeOut       int    `mapstructure:"write_timeout"` // 单条 SQL 的硬超时
	TxTimeout          int    `mapstructure:"tx_timeout"`    // 整个事务的硬超时（建议 = N × WriteTimeOut）
	MaxLifeTime        int    `mapstructure:"max_life_time"`
	MaxOpenConn        int    `mapstructure:"max_open_conn"`
	EnableStatsMonitor bool   `mapstructure:"enable_stats_monitor"`
}

var (
	db  *sql.DB
	dbx *sqlx.DB
)

func (d *Config) Init() *sqlx.DB {
	ctx := context.Background()
	if d.MapOracleDBMonitor == nil {
		d.MapOracleDBMonitor = make(map[string]*OracleDBMonitor)
	}

	//
	d.TxTimeout = 10 * d.WriteTimeOut

	var err error
	addr := fmt.Sprintf("user=\"%s\" password=\"%s\" connectString=\"%s\" %s",
		d.OracleUser, d.OraclePWD, d.OracleConnectStr, d.AppendedOptions)
	db, err = sql.Open("godror", addr)
	if err != nil {
		logs.Fatalf(ctx, "initDB failed: %s", err.Error())
	}

	db.SetMaxOpenConns(d.MaxOpenConn)
	db.SetMaxIdleConns(d.MaxIdleConn)
	db.SetConnMaxLifetime(time.Duration(d.MaxLifeTime) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(d.MaxIdleTime) * time.Minute)
	if err = db.Ping(); err != nil {
		logs.Fatalf(ctx, "init oracle DB failed: %s", err.Error())
	}
	dbx = sqlx.NewDb(db, "godror")
	logs.Info(ctx, "✅ OracleDB connected successfully (sql.DB and sqlx.DB)")

	//nolint:gosec
	monitorCtx, cancel := context.WithCancel(context.Background())
	m := &OracleDBMonitor{ctx: monitorCtx, cancel: cancel}
	if d.EnableStatsMonitor {
		m.wg.Add(1)
		go d.monitorOraclePool(monitorCtx, db, &m.wg, "oracleMain")
		logs.Info(ctx, "📊 OracleDB stats monitor started (interval: %v)", d.StatsInterval)
	}
	d.MapOracleDBMonitor["oracleDB"] = m

	return dbx
}

func (d *Config) monitorOraclePool(ctx context.Context, db *sql.DB, wg *sync.WaitGroup, desc string) {
	defer wg.Done()
	defer gos.Recover()

	ticker := time.NewTicker(time.Duration(d.StatsInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			stats := db.Stats()
			logs.Info(ctx, "[%s] DB Pool Stats: MaxOpen=%d Open=%d InUse=%d Idle=%d WaitCount=%d WaitDuration=%v MaxIdleClosed=%d MaxLifetimeClosed=%d",
				desc,
				stats.MaxOpenConnections,
				stats.OpenConnections,
				stats.InUse,
				stats.Idle,
				stats.WaitCount,
				stats.WaitDuration,
				stats.MaxIdleClosed,
				stats.MaxLifetimeClosed,
			)

			useRate := float64(stats.InUse) / float64(stats.MaxOpenConnections) * 100
			if useRate > 80 {
				logs.Warn(ctx, "[%v] DB pool reach warning level(80%%), inUse: %d, MaxOpenConnections: %d", desc, stats.InUse, stats.MaxOpenConnections)
			}
			if stats.WaitCount > 0 {
				logs.Warn(ctx, "[%v] MaxOpenConnections=%v, waitCount: %d, waitDuration: %v", desc, stats.MaxOpenConnections, stats.WaitCount, stats.WaitDuration)
			}

			metrics.DbPoolMaxOpen.WithLabelValues(desc).Set(float64(stats.MaxOpenConnections))
			metrics.DbPoolOpen.WithLabelValues(desc).Set(float64(stats.OpenConnections))
			metrics.DbPoolInUse.WithLabelValues(desc).Set(float64(stats.InUse))
			metrics.DbPoolIdle.WithLabelValues(desc).Set(float64(stats.Idle))
			metrics.DbPoolWaitCount.WithLabelValues(desc).Set(float64(stats.WaitCount))
			metrics.DbPoolWaitDuration.WithLabelValues(desc).Set(stats.WaitDuration.Seconds())
			metrics.DbPoolMaxIdleClosed.WithLabelValues(desc).Set(float64(stats.MaxIdleClosed))
			metrics.DbPoolMaxLifetimeClosed.WithLabelValues(desc).Set(float64(stats.MaxLifetimeClosed))
			metrics.DbPoolUsageRate.WithLabelValues(desc).Set(useRate)

		case <-ctx.Done():
			logs.Info(ctx, "Stopping Oracle pool monitor...")
			return
		}
	}
}

// GetDbx returns the sqlx.DB handle initialized by Init.
func (d *Config) GetDbx() *sqlx.DB {
	return dbx
}

// Close tears down the Oracle connection pool in the correct order:
//  1. Cancel and drain all monitor goroutines first — they call db.Stats() in
//     a loop, so closing the DB before they exit would cause a use-after-close.
//  2. Close the underlying *sql.DB after every goroutine has exited.
//  3. Nil both globals so that a second call is a safe no-op.
func (d *Config) Close() {
	// Step 1: stop monitor goroutines before touching the connection
	for _, m := range d.MapOracleDBMonitor {
		if m != nil {
			m.cancel()
			m.wg.Wait()
		}
	}
	// Step 2: close the connection now that no goroutine is using it
	if db != nil {
		if err := db.Close(); err != nil {
			logs.Err(context.Background(), "oracle db close error: %v", err)
		}
		// Step 3: nil globals so Close() is idempotent and GetDbx() returns nil
		db = nil
		dbx = nil
	}
}

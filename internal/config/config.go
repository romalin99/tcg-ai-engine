// Package config 加载 TOML 配置（参照 tcg-ucs-fe 的初始化框架：viper + ENV 选择
// config/{dev,sit,prod}.toml，Config 聚合各 pkg 子配置；本项目扩展 [rules] 段
// 描述规则引擎数据源）。
package config

import (
	"context"
	"fmt"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"tcg-ai-engine/pkg/bigcache"
	"tcg-ai-engine/pkg/consul"
	"tcg-ai-engine/pkg/kafka"
	"tcg-ai-engine/pkg/logs"
	od "tcg-ai-engine/pkg/oracle"
	"tcg-ai-engine/pkg/pprof"
	rc "tcg-ai-engine/pkg/redis"
	"tcg-ai-engine/pkg/telemetry"
)

// AppTimeouts 路由级超时档位（秒），配 fiber timeout 中间件使用
type AppTimeouts struct {
	Quick  int `mapstructure:"quick"`
	Normal int `mapstructure:"normal"`
	Long   int `mapstructure:"long"`
	Upload int `mapstructure:"upload"`
}

// RulesConfig 规则引擎数据源（本项目特有段）
type RulesConfig struct {
	Source                string            `mapstructure:"source"` // file（默认）/ oracle
	File                  RulesFileSource   `mapstructure:"file"`
	Oracle                RulesOracleSource `mapstructure:"oracle"`
	ReloadIntervalSeconds int               `mapstructure:"reload_interval_seconds"` // 热更新轮询间隔，<=0 关闭
	GRLTrace              bool              `mapstructure:"grl_trace"`               // true 时把每个 cycle 的候选/执行规则打到 debug 日志
}

type RulesFileSource struct {
	Dir string `mapstructure:"dir"` // *.grl 所在目录
}

type RulesOracleSource struct {
	// go-ora 连接串：oracle://user:pass@host:1521/service_name
	DSN   string `mapstructure:"dsn"`
	Table string `mapstructure:"table"` // 规则表名，默认 RISK_RULES
}

type Config struct {
	TracerProvider *sdktrace.TracerProvider

	Name string `mapstructure:"name"`
	Env  string `mapstructure:"env"`
	Host string `mapstructure:"host"`

	Consul    consul.Config       `mapstructure:"consul"`
	Pprof     pprof.Config        `mapstructure:"pprof"`
	Telemetry telemetry.Telemetry `mapstructure:"telemetry"`
	Redis     rc.Config           `mapstructure:"redis"`
	Log       logs.Config         `mapstructure:"log"`
	OracleIns od.Config           `mapstructure:"oracle"`
	Kafka     kafka.Config        `mapstructure:"kafka"`
	BigCache  bigcache.Config     `mapstructure:"bigcache"`

	AppTimeouts AppTimeouts `mapstructure:"timeouts"`
	Rules       RulesConfig `mapstructure:"rules"`

	Timeout         int64 `mapstructure:"timeout"`   // fiber 读写超时（秒）
	BodyLimit       int   `mapstructure:"bodyLimit"` // 请求体上限（字节）
	ShutdownTimeout int   `mapstructure:"shutdownTimeout"`
	Port            int   `mapstructure:"port"`
}

// InitLog 初始化全局日志单例（主日志 + 可选行为日志）
func (c *Config) InitLog() *logs.Logger {
	c.Log.Env = c.Env
	return logs.NewLogger(c.Log)
}

// ValidateRules 校验规则数据源配置（fail-fast，规则装不进来服务没有意义）
func (c *Config) ValidateRules() error {
	switch c.Rules.Source {
	case "file":
		if c.Rules.File.Dir == "" {
			return fmt.Errorf("rules.source=file 时必须配置 rules.file.dir")
		}
	case "oracle":
		if c.Rules.Oracle.DSN == "" {
			return fmt.Errorf("rules.source=oracle 时必须配置 rules.oracle.dsn")
		}
	default:
		return fmt.Errorf("不支持的规则来源 rules.source=%q（file / oracle）", c.Rules.Source)
	}
	return nil
}

// ReloadInterval 规则热更新轮询间隔
func (c *Config) ReloadInterval() time.Duration {
	return time.Duration(c.Rules.ReloadIntervalSeconds) * time.Second
}

// ShutdownDuration 优雅停机总超时
func (c *Config) ShutdownDuration() time.Duration {
	if c.ShutdownTimeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(c.ShutdownTimeout) * time.Second
}

// Close 释放 Config 持有的资源。每步独立超时/守卫，
// 后端不可达（如 OTLP collector）不能阻塞进程退出。
func (c *Config) Close() {
	c.OracleIns.Close() // godror 池未 Init 时是安全 no-op
	c.Telemetry.Close() // 内部自带 5s 超时

	// TracerProvider.Shutdown 把在途 span 刷给 OTLP 后端，显式 5s 兜底
	if c.TracerProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.TracerProvider.Shutdown(ctx); err != nil {
			logs.Warn(context.Background(), "error shutting down TracerProvider: %v", err)
		}
	}
}

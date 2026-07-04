// Package config 加载 TOML 配置（参考 tcg-ucs-fe 的 config 组织方式，
// 用轻量的 BurntSushi/toml 代替 viper）。
package config

import (
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server ServerConfig `toml:"server"`
	Log    LogConfig    `toml:"log"`
	Rules  RulesConfig  `toml:"rules"`
}

type ServerConfig struct {
	Addr            string `toml:"addr"`             // 监听地址，如 :8080
	ShutdownSeconds int    `toml:"shutdown_seconds"` // 优雅停机等待秒数
}

type LogConfig struct {
	Level    string `toml:"level"`     // debug / info / warn / error
	GRLTrace bool   `toml:"grl_trace"` // true 时把每个 cycle 的候选/执行规则打到 debug 日志
}

type RulesConfig struct {
	Source                string       `toml:"source"`                  // file（默认） / oracle
	ReloadIntervalSeconds int          `toml:"reload_interval_seconds"` // 热更新轮询间隔，<=0 关闭
	File                  FileSource   `toml:"file"`
	Oracle                OracleSource `toml:"oracle"`
}

type FileSource struct {
	Dir string `toml:"dir"` // *.grl 所在目录
}

type OracleSource struct {
	// go-ora 连接串：oracle://user:pass@host:1521/service_name
	DSN   string `toml:"dsn"`
	Table string `toml:"table"` // 规则表名，默认 RISK_RULES
}

// Load 读取并校验配置文件
func Load(path string) (*Config, error) {
	cfg := &Config{
		// 缺省值：本地起服务开箱即用
		Server: ServerConfig{Addr: ":8080", ShutdownSeconds: 10},
		Log:    LogConfig{Level: "info"},
		Rules: RulesConfig{
			Source:                "file",
			ReloadIntervalSeconds: 5,
			File:                  FileSource{Dir: "rules"},
			Oracle:                OracleSource{Table: "RISK_RULES"},
		},
	}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	switch cfg.Rules.Source {
	case "file":
		if cfg.Rules.File.Dir == "" {
			return nil, fmt.Errorf("rules.source=file 时必须配置 rules.file.dir")
		}
	case "oracle":
		if cfg.Rules.Oracle.DSN == "" {
			return nil, fmt.Errorf("rules.source=oracle 时必须配置 rules.oracle.dsn")
		}
	default:
		return nil, fmt.Errorf("不支持的规则来源 rules.source=%q（file / oracle）", cfg.Rules.Source)
	}
	return cfg, nil
}

// ReloadInterval 轮询间隔
func (c *Config) ReloadInterval() time.Duration {
	return time.Duration(c.Rules.ReloadIntervalSeconds) * time.Second
}

// ShutdownTimeout 优雅停机超时
func (c *Config) ShutdownTimeout() time.Duration {
	return time.Duration(c.Server.ShutdownSeconds) * time.Second
}

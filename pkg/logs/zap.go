package logs

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New 按级别创建开发友好格式的 *zap.Logger，与 NewLogger 的全局单例互不影响。
// internal/* 与 grule 的 ast.SetLogger 仍直接消费 *zap.Logger；
// 切换到本包的 ctx 日志 API（logs.Info 等）前，保留此兼容入口。
// 注意：grule 的 GRL 内置 Log()/LogFormat() 走的是 ast.SetLogger 注入的实例，
// debug 级别下 grule 自身会打印海量 AST 求值日志，排障时再开。
func New(level string) (*zap.Logger, error) {
	var lv zapcore.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("非法日志级别 %q: %w", level, err)
	}
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(lv)
	return cfg.Build()
}

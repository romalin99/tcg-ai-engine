// Package logs zap 日志初始化（对齐 tcg-ucs-fe 的 pkg/logs 角色）
package logs

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New 按级别创建开发友好格式的 zap logger。
// 注意：grule 的 GRL 内置 Log()/LogFormat() 走的是 ast.SetLogger 注入的实例，
// debug 级别下 grule 自身会打印海量 AST 求值日志，排障时再开。
func New(level string) (*zap.Logger, error) {
	var lv zapcore.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("非法日志级别 %q: %w", level, err)
	}
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(lv)
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	return cfg.Build()
}

package engine

import (
	"context"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	grule "github.com/hyperjumptech/grule-rule-engine/engine"
	"go.uber.org/zap"
)

// traceListener 实现 grule 的 GruleEngineListener，把推理循环的关键事件
// 打到 debug 日志：每个 cycle 谁进了候选集（冲突集）、冲突消解后谁被执行。
// 挂在共享的 GruleEngine 上，会被多个 goroutine 并发回调——只做无状态日志。
type traceListener struct {
	logger *zap.Logger
}

func newTraceListener(logger *zap.Logger) *traceListener {
	return &traceListener{logger: logger}
}

// BeginCycle 每个推理 cycle 开始时回调
func (t *traceListener) BeginCycle(_ context.Context, cycle uint64) {
	t.logger.Debug("规则推理 cycle 开始", zap.Uint64("cycle", cycle))
}

// EvaluateRuleEntry 每条规则的 when 求值完毕后回调；candidate=true 表示进入候选集
func (t *traceListener) EvaluateRuleEntry(_ context.Context, cycle uint64, entry *ast.RuleEntry, candidate bool) {
	if candidate {
		t.logger.Debug("规则进入候选集",
			zap.Uint64("cycle", cycle),
			zap.String("rule", entry.RuleName),
			zap.Int64("salience", int64(entry.Salience)))
	}
}

// ExecuteRuleEntry 冲突消解选出 salience 最高的规则、即将执行其 then 时回调
func (t *traceListener) ExecuteRuleEntry(_ context.Context, cycle uint64, entry *ast.RuleEntry) {
	t.logger.Debug("规则执行",
		zap.Uint64("cycle", cycle),
		zap.String("rule", entry.RuleName))
}

var _ grule.GruleEngineListener = (*traceListener)(nil)

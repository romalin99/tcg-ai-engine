// Package service 业务编排层：组装 Fact → 跑规则 → Go 侧结算。
package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/romalin99/tcg-ai-engine/internal/engine"
	"github.com/romalin99/tcg-ai-engine/internal/model"
	"github.com/romalin99/tcg-ai-engine/internal/types/req"
)

// RiskService 风控评估服务
type RiskService struct {
	eng    *engine.Engine
	logger *zap.Logger
}

func NewRiskService(eng *engine.Engine, logger *zap.Logger) *RiskService {
	return &RiskService{eng: eng, logger: logger}
}

// Evaluate 对一笔订单执行全量规则推理，返回规则写好的 Result。
// Fact 的注册名（Order/Customer/Product/Merchant/Result）必须与 GRL 里
// 引用的名字一致；Result 是唯一的"输出"事实，规则只写它，不回写输入。
func (s *RiskService) Evaluate(ctx context.Context, request *req.EvaluateRequest) (*model.Result, error) {
	result := model.NewResult(request.Order.Freight)
	facts := map[string]any{
		"Order":    request.Order,
		"Customer": request.Customer,
		"Product":  request.Product,
		"Merchant": request.Merchant,
		"Result":   result,
	}

	start := time.Now()
	if err := s.eng.Evaluate(ctx, facts); err != nil {
		return nil, err
	}
	// 规则只负责"定参数"（折扣率、运费、积分倍率），最终结算留在 Go 代码里
	result.Finalize(request.Order.Amount)

	s.logger.Info("风控评估完成",
		zap.String("order_id", request.Order.ID),
		zap.String("decision", result.Decision),
		zap.Float64("risk_score", result.RiskScore),
		zap.Strings("hit_rules", result.HitRules),
		zap.Duration("cost", time.Since(start)))
	return result, nil
}

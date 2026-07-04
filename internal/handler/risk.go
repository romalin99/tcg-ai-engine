// Package handler HTTP 处理器（对齐 tcg-ucs-fe 的 internal/handler）
package handler

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/romalin99/tcg-ai-engine/internal/engine"
	"github.com/romalin99/tcg-ai-engine/internal/service"
	"github.com/romalin99/tcg-ai-engine/internal/types/req"
	"github.com/romalin99/tcg-ai-engine/internal/types/resp"
)

// Risk 风控评估接口
type Risk struct {
	svc    *service.RiskService
	eng    *engine.Engine
	logger *zap.Logger
}

func NewRisk(svc *service.RiskService, eng *engine.Engine, logger *zap.Logger) *Risk {
	return &Risk{svc: svc, eng: eng, logger: logger}
}

// Evaluate POST /api/v1/risk/evaluate
func (h *Risk) Evaluate(c fiber.Ctx) error {
	var request req.EvaluateRequest
	// Bind().JSON 不看 Content-Type 强按 JSON 解析，与旧版 json.Decoder 行为一致
	if err := c.Bind().JSON(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(resp.Err(40001, "请求体不是合法 JSON: "+err.Error()))
	}
	if err := request.Validate(); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(resp.Err(40002, err.Error()))
	}

	result, err := h.svc.Evaluate(c.Context(), &request)
	if err != nil {
		if errors.Is(err, engine.ErrNotReady) {
			return c.Status(fiber.StatusServiceUnavailable).JSON(resp.Err(50301, err.Error()))
		}
		h.logger.Error("风控评估失败", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(resp.Err(50001, err.Error()))
	}

	info, _ := h.eng.Info() // 刚成功执行过规则，引擎必然已就绪
	return c.JSON(resp.OK(resp.EvaluateData{
		OrderID: request.Order.ID,
		Result:  result,
		Engine:  resp.EngineMeta{Checksum: info.Checksum, RuleCount: info.RuleCount},
	}))
}

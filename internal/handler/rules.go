package handler

import (
	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"tcg-ai-engine/internal/engine"
	"tcg-ai-engine/internal/types/resp"
)

// Rules 规则管理接口：查看当前版本、手动触发热更新
type Rules struct {
	eng      *engine.Engine
	reloader *engine.Reloader
	logger   *zap.Logger
}

func NewRules(eng *engine.Engine, reloader *engine.Reloader, logger *zap.Logger) *Rules {
	return &Rules{eng: eng, reloader: reloader, logger: logger}
}

// Info 当前生效规则集：来源、指纹、规则清单
//
//	@Summary  查看当前生效规则集
//	@Tags     rules
//	@Produce  json
//	@Success  200 {object} resp.Envelope{data=engine.Info}
//	@Router   /api/v1/rules [get]
func (h *Rules) Info(c fiber.Ctx) error {
	info, err := h.eng.Info()
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(resp.Err(50301, err.Error()))
	}
	return c.JSON(resp.OK(info))
}

// Reload 手动触发一次热更新（与后台轮询同一条链路）
//
//	@Summary  手动触发规则热更新
//	@Tags     rules
//	@Produce  json
//	@Success  200 {object} resp.Envelope{data=resp.ReloadData}
//	@Router   /api/v1/rules/reload [post]
func (h *Rules) Reload(c fiber.Ctx) error {
	info, changed, err := h.reloader.ReloadOnce(c.Context())
	if err != nil {
		h.logger.Error("手动热更新失败", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(resp.Err(50002, err.Error()))
	}
	return c.JSON(resp.OK(resp.ReloadData{Changed: changed, Info: info}))
}

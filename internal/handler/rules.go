package handler

import (
	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/romalin99/tcg-ai-engine/internal/engine"
	"github.com/romalin99/tcg-ai-engine/internal/types/resp"
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

// Info GET /api/v1/rules —— 当前生效规则集：来源、指纹、规则清单
func (h *Rules) Info(c fiber.Ctx) error {
	info, err := h.eng.Info()
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(resp.Err(50301, err.Error()))
	}
	return c.JSON(resp.OK(info))
}

// Reload POST /api/v1/rules/reload —— 手动触发一次热更新（与后台轮询同一条链路）
func (h *Rules) Reload(c fiber.Ctx) error {
	info, changed, err := h.reloader.ReloadOnce(c.Context())
	if err != nil {
		h.logger.Error("手动热更新失败", zap.Error(err))
		return c.Status(fiber.StatusInternalServerError).JSON(resp.Err(50002, err.Error()))
	}
	return c.JSON(resp.OK(resp.ReloadData{Changed: changed, Info: info}))
}

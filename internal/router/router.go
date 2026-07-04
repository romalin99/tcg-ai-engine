// Package router 路由注册（对齐 tcg-ucs-fe 的 internal/router）
package router

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"tcg-ai-engine/internal/config"
	"tcg-ai-engine/internal/handler"
	"tcg-ai-engine/internal/middleware"
	"tcg-ai-engine/internal/types/resp"
)

// New 组装 fiber 应用：统一错误响应 + 中间件链 + 路由 + 监控/文档端点
func New(cfg *config.Config, risk *handler.Risk, rules *handler.Rules, logger *zap.Logger) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName: "tcg-ai-engine",
		// 框架层错误（404/405 等）也回统一 JSON 壳，业务码 = HTTP 状态码 ×100
		ErrorHandler: func(c fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			var e *fiber.Error
			if errors.As(err, &e) {
				code = e.Code
			}
			return c.Status(code).JSON(resp.Err(code*100, err.Error()))
		},
	})

	// 中间件从外到内：Recover 最外层兜底，AccessLog 记录全部请求
	app.Use(middleware.Recover(logger))
	app.Use(middleware.AccessLog(logger))

	// Prometheus 指标（/metrics、/monitor、/livez、/readyz）与 Swagger UI（/swagger/）
	RegisterPrometheus(app, cfg)
	InitSwagger(app, cfg)

	api := app.Group("/api/v1")
	api.Post("/risk/evaluate", risk.Evaluate)
	api.Get("/rules", rules.Info)
	api.Post("/rules/reload", rules.Reload)

	app.Get("/healthz", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	return app
}

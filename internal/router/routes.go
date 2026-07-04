package router

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/timeout"

	"tcg-ai-engine/internal/config"
	"tcg-ai-engine/internal/handler"
)

// RegisterHandlers 注册业务路由（组级限流 + 路由级超时，
// 超时档位来自 [timeouts]）。
func RegisterHandlers(app *fiber.App, risk *handler.Risk, rules *handler.Rules, c *config.Config) {
	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/ping", func(c fiber.Ctx) error { return c.SendString("pong") })

	groupLimiter := limiter.New(limiter.Config{
		Max:        800,
		Expiration: time.Second,
		KeyGenerator: func(fiber.Ctx) string {
			return "global"
		},
	})

	api := app.Group("/api/v1", groupLimiter)

	quickTimeout := timeout.Config{Timeout: time.Duration(c.AppTimeouts.Quick) * time.Second}
	normalTimeout := timeout.Config{Timeout: time.Duration(c.AppTimeouts.Normal) * time.Second}

	api.Post("/risk/evaluate", timeout.New(risk.Evaluate, normalTimeout))
	api.Get("/rules", timeout.New(rules.Info, quickTimeout))
	api.Post("/rules/reload", timeout.New(rules.Reload, normalTimeout))
}

// Package middleware HTTP 中间件
package middleware

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"go.uber.org/zap"
)

// AccessLog 访问日志中间件。
// fiber 的全局 ErrorHandler 要等整条链返回后才写响应，直接透传 error 会让这里
// 读到未定稿的状态码；因此先把 error 交给 ErrorHandler 写完响应再记录日志，
// 并返回 nil 避免框架二次处理。
func AccessLog(logger *zap.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		if err := c.Next(); err != nil {
			if handleErr := c.App().Config().ErrorHandler(c, err); handleErr != nil {
				_ = c.SendStatus(fiber.StatusInternalServerError)
			}
		}
		logger.Info("access",
			zap.String("method", c.Method()),
			zap.String("path", c.Path()),
			zap.Int("status", c.Response().StatusCode()),
			zap.Duration("cost", time.Since(start)))
		return nil
	}
}

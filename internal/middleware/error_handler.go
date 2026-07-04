package middleware

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	"tcg-ai-engine/internal/types/resp"
)

// ErrorHandler 全局错误处理：框架层错误（404/405/408/413 等）也返回统一 JSON 壳，
// 业务码 = HTTP 状态码 ×100，与 handler 里的 4xxxx/5xxxx 编码体系一致。
func ErrorHandler(c fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	var e *fiber.Error
	if errors.As(err, &e) {
		code = e.Code
	}
	return c.Status(code).JSON(resp.Err(code*100, err.Error()))
}

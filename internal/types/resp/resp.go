// Package resp HTTP 响应体定义（对齐 tcg-ucs-fe 的 internal/types/resp）
package resp

import (
	"tcg-ai-engine/internal/engine"
	"tcg-ai-engine/internal/model"
)

// Envelope 统一响应壳
type Envelope struct {
	Code    int    `json:"code"` // 0 成功，非 0 失败
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func OK(data any) Envelope {
	return Envelope{Code: 0, Message: "ok", Data: data}
}

func Err(code int, msg string) Envelope {
	return Envelope{Code: code, Message: msg}
}

// EvaluateData 评估结果 = 规则输出 + 当时生效的规则版本指纹（便于回溯）
type EvaluateData struct {
	OrderID string        `json:"order_id"`
	Result  *model.Result `json:"result"`
	Engine  EngineMeta    `json:"engine"`
}

// EngineMeta 精简的引擎版本信息
type EngineMeta struct {
	Checksum  string `json:"checksum"`
	RuleCount int    `json:"rule_count"`
}

// ReloadData 手动热更新结果
type ReloadData struct {
	Changed bool        `json:"changed"` // false = 内容没变，未重建
	Info    engine.Info `json:"info"`
}

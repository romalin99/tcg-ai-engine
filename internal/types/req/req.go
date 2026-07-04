// Package req HTTP 请求体定义
package req

import "tcg-ai-engine/internal/model"

// EvaluateRequest 风控评估请求：四个 Fact 缺一不可。
// 字段直接复用 model 的 JSON 标签，避免 DTO 与领域模型两套定义来回搬运。
type EvaluateRequest struct {
	Order    *model.Order    `json:"order"`
	Customer *model.Customer `json:"customer"`
	Product  *model.Product  `json:"product"`
	Merchant *model.Merchant `json:"merchant"`
}

// Validate 四个 Fact 都必须提供：规则的 when 会引用全部对象，
// 缺任何一个都会导致规则求值失败。
func (r *EvaluateRequest) Validate() error {
	switch {
	case r.Order == nil:
		return errMissing("order")
	case r.Customer == nil:
		return errMissing("customer")
	case r.Product == nil:
		return errMissing("product")
	case r.Merchant == nil:
		return errMissing("merchant")
	}
	return nil
}

type missingErr string

func errMissing(field string) error { return missingErr(field) }

func (e missingErr) Error() string { return "缺少必填字段: " + string(e) }

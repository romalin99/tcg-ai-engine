package model

import "slices"

// Product 商品事实（fact）
type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"` // 类目：electronics / gift_card / luxury / books ...
	Price       float64 `json:"price"`
	Stock       int     `json:"stock"`        // 库存
	IsVirtual   bool    `json:"is_virtual"`   // 虚拟商品（卡密、点卡等），无物流、易套现
	Restricted  bool    `json:"restricted"`   // 管制/限购商品
	PresaleDays int     `json:"presale_days"` // 预售天数，0 表示现货
}

// IsOutOfStock 无库存
func (p *Product) IsOutOfStock() bool {
	return p.Stock <= 0
}

// highRiskCategories 高风险类目：易套现、欺诈高发
var highRiskCategories = []string{"gift_card", "luxury", "digital"}

// IsHighRiskCategory 是否高风险类目。
// 多条件/名单类判断封装在 Go 方法里，GRL 中只写 Product.IsHighRiskCategory()，
// 规则更易读，名单调整也不用改规则文件。
func (p *Product) IsHighRiskCategory() bool {
	return slices.Contains(highRiskCategories, p.Category)
}

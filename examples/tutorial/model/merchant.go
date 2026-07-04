package model

// Merchant 商户事实（fact）
type Merchant struct {
	ID          string
	Name        string
	Category    string  // 经营类目
	Rating      float64 // 评分 0~5
	RefundRate  float64 // 退款率 0~1
	YearsActive int     // 经营年数
	Blacklisted bool    // 是否在黑名单
}

// IsHighRisk 高风险商户：黑名单、退款率过高，或新店且低评分。
// 把多条件组合封装在 Go 方法里，GRL 中只写 Merchant.IsHighRisk()，
// 规则更易读，逻辑也可以单独做单元测试。
func (m *Merchant) IsHighRisk() bool {
	return m.Blacklisted ||
		m.RefundRate > 0.20 ||
		(m.YearsActive < 1 && m.Rating < 3.0)
}

// IsTrusted 可信商户：高评分、经营满 3 年、退款率极低
func (m *Merchant) IsTrusted() bool {
	return m.Rating >= 4.5 && m.YearsActive >= 3 && m.RefundRate < 0.05
}

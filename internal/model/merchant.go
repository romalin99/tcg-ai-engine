package model

// Merchant 商户事实（fact）
type Merchant struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`     // 经营类目
	Rating      float64 `json:"rating"`       // 评分 0~5
	RefundRate  float64 `json:"refund_rate"`  // 退款率 0~1
	YearsActive int     `json:"years_active"` // 经营年数
	Blacklisted bool    `json:"blacklisted"`  // 是否在黑名单
}

// IsHighRisk 高风险商户：黑名单、退款率过高，或新店且低评分
func (m *Merchant) IsHighRisk() bool {
	return m.Blacklisted ||
		m.RefundRate > 0.20 ||
		(m.YearsActive < 1 && m.Rating < 3.0)
}

// IsTrusted 可信商户：高评分、经营满 3 年、退款率极低
func (m *Merchant) IsTrusted() bool {
	return m.Rating >= 4.5 && m.YearsActive >= 3 && m.RefundRate < 0.05
}

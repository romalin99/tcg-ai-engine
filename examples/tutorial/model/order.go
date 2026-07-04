package model

type Order struct {
	Amount float64
	VIP    bool

	Discount    float64
	Freight     float64
	FinalAmount float64

	// 风控决策输出：由 rules/risk.grl 里的规则写入
	RiskScore    float64 // 风险分，多条规则可累加
	Rejected     bool    // 是否拒单（拒单后折扣类规则不再触发）
	RejectReason string
}

func (o *Order) Calc() {
	o.FinalAmount = o.Amount*o.Discount + o.Freight
}

func (o *Order) IsBigOrder() bool {
	return o.Amount >= 10000
}

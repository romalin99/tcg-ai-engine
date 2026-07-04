package model

import "math"

// 决策结论。Decision 为空串表示"尚未决策"，
// 决策类规则以 Result.Decision == "" 作为互斥闸门：谁先写入谁生效。
const (
	DecisionApprove = "approve" // 放行
	DecisionReview  = "review"  // 转人工审核
	DecisionReject  = "reject"  // 拒单
)

// Result 规则输出事实（"黑板"模式）：所有规则只写 Result，不回写输入 Fact。
//
// 注意（grule 表达式缓存）：会被规则 when 引用的字段（Decision / RiskScore /
// DiscountName / FreightFree / PointsRate ...），在 then 里必须用 `=` 直接赋值，
// grule 才会失效对应的缓存并在下个 cycle 重新求值；
// 只写不读的字段（HitRules）才可以用方法（AddHit）修改。
type Result struct {
	// 风控
	RiskScore    float64 `json:"risk_score"`    // 风险分，多条规则累加
	Decision     string  `json:"decision"`      // approve / review / reject
	RejectReason string  `json:"reject_reason"` // 拒单原因

	// 营销（互斥组：一单只命中一个折扣档位）
	Discount      float64 `json:"discount"`       // 折扣率，初始 1.0
	DiscountName  string  `json:"discount_name"`  // 命中的折扣档位，空串=未定，作为互斥闸门
	ExtraDiscount bool    `json:"extra_discount"` // 叠加折扣是否已生效（防重复叠加）

	// 运费
	Freight     float64 `json:"freight"`      // 初始为 Order.Freight，规则可减免/加收
	FreightFree bool    `json:"freight_free"` // 免运费闸门：免运费后不再加收偏远地区附加费

	// 积分（互斥组：积分倍率只定一次）
	PointsRate   float64 `json:"points_rate"`   // 积分倍率，0 表示未定，作为互斥闸门
	PointsEarned int     `json:"points_earned"` // Finalize 时按倍率结算

	// 结算与追踪
	FinalAmount float64  `json:"final_amount"` // 最终应付 = Amount*Discount + Freight
	HitRules    []string `json:"hit_rules"`    // 命中的规则名，按执行顺序
}

// NewResult 构造初始 Result：折扣 1.0（不打折），运费取订单基础运费报价。
func NewResult(baseFreight float64) *Result {
	return &Result{Discount: 1.0, Freight: baseFreight}
}

// AddHit 记录命中的规则名。HitRules 不被任何 when 引用，方法修改是安全的。
func (r *Result) AddHit(name string) {
	r.HitRules = append(r.HitRules, name)
}

// IsRejected 是否已拒单
func (r *Result) IsRejected() bool {
	return r.Decision == DecisionReject
}

// Finalize 规则跑完后的普通业务计算："规则定参数，计算留在 Go"。
// 拒单时应付金额与积分无意义，直接归零。
func (r *Result) Finalize(amount float64) {
	if r.IsRejected() {
		r.FinalAmount = 0
		r.PointsEarned = 0
		return
	}
	r.FinalAmount = math.Round(amount*r.Discount*100)/100 + r.Freight
	r.PointsEarned = int(amount * r.PointsRate)
}

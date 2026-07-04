package model

import "slices"

// Address 嵌套结构：GRL 里通过多级字段访问，如 Customer.Address.Country
type Address struct {
	Country  string `json:"country"`
	Province string `json:"province"`
	City     string `json:"city"`
}

// Customer 客户事实（fact）
type Customer struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Level           int      `json:"level"`  // 会员等级 1~5
	Points          int      `json:"points"` // 当前积分
	Tags            []string `json:"tags"`   // 客户标签，GRL 里通过 Customer.HasTag("xxx") 查询
	Address         Address  `json:"address"`
	RegisterDays    int      `json:"register_days"`    // 注册天数
	Blacklisted     bool     `json:"blacklisted"`      // 是否在黑名单
	TotalOrders     int      `json:"total_orders"`     // 历史订单数
	RefundCount     int      `json:"refund_count"`     // 历史退款次数
	ChargebackCount int      `json:"chargeback_count"` // 拒付（chargeback）次数
}

// IsNew 注册不满 30 天视为新客
func (c *Customer) IsNew() bool {
	return c.RegisterDays < 30
}

// HasTag 判断客户是否带某标签——演示 GRL 里调用带参数的方法
func (c *Customer) HasTag(tag string) bool {
	return slices.Contains(c.Tags, tag)
}

// RefundRatio 历史退款率；无订单时返回 0，避免规则里除零
func (c *Customer) RefundRatio() float64 {
	if c.TotalOrders == 0 {
		return 0
	}
	return float64(c.RefundCount) / float64(c.TotalOrders)
}

// remoteProvinces 偏远地区（运费加收）
var remoteProvinces = []string{"西藏", "新疆", "青海", "内蒙古"}

// IsRemoteArea 收货地址是否偏远地区
func (c *Customer) IsRemoteArea() bool {
	return slices.Contains(remoteProvinces, c.Address.Province)
}

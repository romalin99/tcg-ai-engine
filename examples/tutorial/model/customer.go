package model

import "slices"

// Address 嵌套结构：演示 GRL 里的多级字段访问，如 Customer.Address.Country
type Address struct {
	Country string
	City    string
}

// Customer 客户事实（fact）
type Customer struct {
	ID           string
	Name         string
	Level        int      // 会员等级 1~5
	Points       int      // 积分
	Tags         []string // 客户标签，GRL 里通过 Customer.HasTag("xxx") 查询
	Address      Address  // 嵌套结构
	RegisterDays int      // 注册天数（简化处理，避免在规则里操作 time.Time）
}

// IsNew 注册不满 30 天视为新客
func (c *Customer) IsNew() bool {
	return c.RegisterDays < 30
}

// HasTag 判断客户是否带某标签——演示 GRL 里调用带参数的方法
func (c *Customer) HasTag(tag string) bool {
	return slices.Contains(c.Tags, tag)
}

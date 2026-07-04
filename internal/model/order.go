package model

// Order 订单事实（fact）：规则的主要输入之一。
// 字段全部为可 JSON 反序列化的简单类型，避免在 GRL 里操作 time.Time 等复杂类型。
type Order struct {
	ID        string  `json:"id"`
	Amount    float64 `json:"amount"`         // 订单金额
	Quantity  int     `json:"quantity"`       // 购买件数
	Freight   float64 `json:"freight"`        // 基础运费报价（规则可减免/加收，结果写入 Result.Freight）
	PayMethod string  `json:"pay_method"`     // 支付方式：credit_card / cod / balance ...
	Channel   string  `json:"channel"`        // 下单渠道：app / web / h5
	HourOfDay int     `json:"hour_of_day"`    // 下单时刻（0~23，简化处理避免 time.Time）
	IsFirst   bool    `json:"is_first_order"` // 是否首单
	DeviceID  string  `json:"device_id"`      // 设备指纹，空值视为风险信号
	IPCountry string  `json:"ip_country"`     // 下单 IP 归属国，空表示未知
}

// IsBigOrder 大额订单
func (o *Order) IsBigOrder() bool {
	return o.Amount >= 10000
}

// IsNightOrder 凌晨订单（0~6 点），盗刷高发时段
func (o *Order) IsNightOrder() bool {
	return o.HourOfDay >= 0 && o.HourOfDay < 6
}

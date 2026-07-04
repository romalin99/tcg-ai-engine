package model

import "time"

// Rule 一份规则资源 = 一段 GRL 文本（可含多条 rule）。
// 文件源：一个 .grl 文件对应一个 Rule；Oracle 源：RISK_RULES 表的一行对应一个 Rule。
type Rule struct {
	Name      string    // 资源名：文件名或表中 RULE_NAME
	Content   string    // GRL 文本
	UpdatedAt time.Time // 最后更新时间（文件 mtime / 表 UPDATED_AT），仅用于展示
}

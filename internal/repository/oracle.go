package repository

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"tcg-ai-engine/internal/model"
)

// OracleRepository 从 Oracle 规则表加载启用的规则。
// 表结构见 scripts/sql/rules_table.sql；一行 = 一段 GRL 文本（对应一个 .grl 文件），
// 用 cmd/ruleloader 可以把 rules/*.grl 批量导入表中。
type OracleRepository struct {
	db    *sql.DB
	table string
}

// 表名来自配置文件，仅允许合法标识符，防止拼接出坏 SQL
var identRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.$]*$`)

func NewOracleRepository(db *sql.DB, table string) (*OracleRepository, error) {
	if table == "" {
		table = "RISK_RULES"
	}
	if !identRe.MatchString(table) {
		return nil, fmt.Errorf("非法的规则表名: %q", table)
	}
	return &OracleRepository{db: db, table: table}, nil
}

func (r *OracleRepository) Load(ctx context.Context) ([]model.Rule, error) {
	// ORDER BY 保证顺序确定，内容指纹才稳定
	query := fmt.Sprintf(
		"SELECT RULE_NAME, GRL_CONTENT, UPDATED_AT FROM %s WHERE ENABLED = 1 ORDER BY RULE_NAME",
		r.table,
	)
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询规则表 %s: %w", r.table, err)
	}
	defer rows.Close()

	var rules []model.Rule
	for rows.Next() {
		var rule model.Rule
		if err := rows.Scan(&rule.Name, &rule.Content, &rule.UpdatedAt); err != nil {
			return nil, fmt.Errorf("扫描规则行: %w", err)
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历规则行: %w", err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("规则表 %s 中没有启用的规则", r.table)
	}
	return rules, nil
}

func (r *OracleRepository) Source() string {
	return "oracle:" + r.table
}

// Package repository 提供规则的数据源抽象：
// 引擎不关心规则存在哪里，只管拿到一组 GRL 文本。
// 默认从 rules/ 目录加载 *.grl；也可从 Oracle 的规则表加载。
// 热更新轮询复用同一个 Load()：加载结果的内容指纹变了才重建 KnowledgeBase。
package repository

import (
	"context"

	"github.com/romalin99/tcg-ai-engine/internal/model"
)

// RuleRepository 规则数据源。
// Load 必须返回确定性的顺序（按 Name 排序），保证内容指纹稳定。
type RuleRepository interface {
	// Load 返回当前全量启用的规则
	Load(ctx context.Context) ([]model.Rule, error)
	// Source 数据源描述，仅用于日志与接口展示
	Source() string
}

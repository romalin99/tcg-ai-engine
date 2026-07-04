// Package engine 封装 grule-rule-engine，对外只暴露两个动作：
//
//	Evaluate —— 并发安全地执行一次规则推理
//	Reload   —— 原子地热替换整套规则（构建失败保留旧版本）
//
// 并发模型：
//   - GruleEngine 本身无状态，可全局共享；
//   - KnowledgeBase 实例有执行期状态（Retract 标记、表达式缓存），不能被两个
//     goroutine 同时使用，但 grule 在每次 Execute 开头会 Reset，因此可以用
//     sync.Pool 复用实例，省掉每次请求克隆 AST 的开销；
//   - 热更新通过 atomic.Pointer 整体换掉 snapshot（规则库+专属 Pool），
//     换版本瞬间：进行中的请求继续用旧 snapshot 跑完，新请求拿到新 snapshot，
//     旧版本无人引用后被 GC，全程无锁、无需停服。
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	grule "github.com/hyperjumptech/grule-rule-engine/engine"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
	"go.uber.org/zap"

	"tcg-ai-engine/internal/model"
)

// KnowledgeBase 在 KnowledgeLibrary 中的注册键。
// 每次热更新都新建一个 Library，所以键可以固定不变；
// 版本追踪靠内容指纹（Info.Checksum），不靠这里的版本号。
const (
	kbName    = "risk-rules"
	kbVersion = "1.0.0"
)

// ErrNotReady 引擎尚未成功加载过任何规则
var ErrNotReady = errors.New("规则引擎尚未加载规则")

// Info 当前生效规则集的元信息（供 GET /rules 与日志使用）
type Info struct {
	Source    string    `json:"source"`     // 规则来源：file:rules / oracle:RISK_RULES
	Checksum  string    `json:"checksum"`   // 全量 GRL 内容的 SHA-256，热更新判据
	RuleCount int       `json:"rule_count"` // 规则条数（rule 级别，非文件数）
	RuleNames []string  `json:"rule_names"` // 所有规则名，字典序
	LoadedAt  time.Time `json:"loaded_at"`  // 本版本生效时间
}

// snapshot 一个规则版本的全部运行期资产。
// pool 与版本绑定：热更新换掉整个 snapshot，旧版本的实例随旧 pool 一起消亡，
// 不存在"新请求拿到旧规则实例"的窗口。
type snapshot struct {
	lib  *ast.KnowledgeLibrary
	pool sync.Pool
	info Info
}

// acquire 从池中取一个 KnowledgeBase 实例，池空时克隆一个新的
func (s *snapshot) acquire() (*ast.KnowledgeBase, error) {
	if kb, ok := s.pool.Get().(*ast.KnowledgeBase); ok && kb != nil {
		return kb, nil
	}
	return s.lib.NewKnowledgeBaseInstance(kbName, kbVersion)
}

// Engine 并发安全的规则引擎
type Engine struct {
	grule   *grule.GruleEngine
	current atomic.Pointer[snapshot]
	logger  *zap.Logger
}

// New 创建引擎；traceRules 为 true 时挂载监听器，
// 把每个 cycle 的候选/执行规则打到 debug 日志（排障用，生产建议关闭）。
func New(logger *zap.Logger, traceRules bool) *Engine {
	g := grule.NewGruleEngine() // MaxCycle 默认 5000，规则忘写 Retract 时兜底报错
	if traceRules {
		g.Listeners = []grule.GruleEngineListener{newTraceListener(logger)}
	}
	return &Engine{grule: g, logger: logger}
}

// Reload 用一组规则文本重建规则库并原子切换。
// 返回值 changed=false 表示内容指纹没变、跳过重建。
// 构建/校验失败时返回错误且不切换——当前版本继续服务（热更新的安全底线）。
func (e *Engine) Reload(rules []model.Rule, source string) (Info, bool, error) {
	sum := checksum(rules)
	if cur := e.current.Load(); cur != nil && cur.info.Checksum == sum {
		return cur.info, false, nil
	}

	lib := ast.NewKnowledgeLibrary()
	rb := builder.NewRuleBuilder(lib)
	resources := make([]pkg.Resource, 0, len(rules))
	for _, r := range rules {
		resources = append(resources, pkg.NewBytesResource([]byte(r.Content)))
	}
	// 语法错误、跨文件规则名重复，都在这一步暴露
	if err := rb.BuildRuleFromResources(kbName, kbVersion, resources); err != nil {
		return Info{}, false, fmt.Errorf("构建规则库失败: %w", err)
	}

	// 先实例化一次做校验，顺便取规则名清单；这个实例直接放进新池预热
	kb, err := lib.NewKnowledgeBaseInstance(kbName, kbVersion)
	if err != nil {
		return Info{}, false, fmt.Errorf("实例化 KnowledgeBase 失败: %w", err)
	}
	names := make([]string, 0, len(kb.RuleEntries))
	for name := range kb.RuleEntries {
		names = append(names, name)
	}
	sort.Strings(names)

	next := &snapshot{
		lib: lib,
		info: Info{
			Source:    source,
			Checksum:  sum,
			RuleCount: len(names),
			RuleNames: names,
			LoadedAt:  time.Now(),
		},
	}
	next.pool.New = func() any {
		inst, err := lib.NewKnowledgeBaseInstance(kbName, kbVersion)
		if err != nil {
			// 已通过一次实例化校验，正常不会走到；返回 nil 由 acquire 兜底报错
			e.logger.Error("克隆 KnowledgeBase 失败", zap.Error(err))
			return nil
		}
		return inst
	}
	next.pool.Put(kb)

	e.current.Store(next)
	return next.info, true, nil
}

// Evaluate 执行一次推理。facts 的 key 就是 GRL 里引用的对象名
// （如 "Order"/"Customer"/"Product"/"Merchant"/"Result"），value 必须是指针，
// 否则规则 then 里的赋值改不到原对象。
func (e *Engine) Evaluate(ctx context.Context, facts map[string]any) error {
	snap := e.current.Load()
	if snap == nil {
		return ErrNotReady
	}
	kb, err := snap.acquire()
	if err != nil {
		return fmt.Errorf("获取 KnowledgeBase 实例: %w", err)
	}
	// Execute 开头会 Reset 工作内存与 Retract 状态，脏实例可以安全回池复用
	defer snap.pool.Put(kb)

	dctx := ast.NewDataContext()
	for name, fact := range facts {
		if err := dctx.Add(name, fact); err != nil {
			return fmt.Errorf("注册事实 %s: %w", name, err)
		}
	}
	if err := e.grule.ExecuteWithContext(ctx, dctx, kb); err != nil {
		return fmt.Errorf("规则执行失败: %w", err)
	}
	return nil
}

// Info 返回当前生效规则集的元信息
func (e *Engine) Info() (Info, error) {
	snap := e.current.Load()
	if snap == nil {
		return Info{}, ErrNotReady
	}
	return snap.info, nil
}

// checksum 对全量规则内容做 SHA-256。
// Load() 保证顺序稳定（按 Name 排序），指纹才有可比性。
func checksum(rules []model.Rule) string {
	h := sha256.New()
	for _, r := range rules {
		h.Write([]byte(r.Name))
		h.Write([]byte{0})
		h.Write([]byte(r.Content))
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))
}

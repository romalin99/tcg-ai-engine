// grule 进阶特性示例（go run ./examples/advanced）：
//
//  1. 链式调用：Fact.Func().Field —— 方法返回对象后继续取字段/再调方法；
//  2. 数组 / map / 嵌套结构访问：Fact.SubFacts[1].SubFacts[2].AnIntArray[12]、
//     Fact.SubMaps["Key"].AnIntArray[0]——注意：裸下标越界会在求值时 panic；
//     当前 grule(v1.20.4) 引擎会 recover 并把它转成该条规则的评估错误、静默跳过
//     （进程不崩、Execute 不报错，只有注入 logger 才看得到），规则等于悄悄失效——
//     所以生产环境必须在 Fact 层做边界保护（见 SubInt/MapInt 安全访问器）；
//  3. JSON 直接定义 Fact（DataContext.AddJSON）：无需预定义 Go struct，
//     适合规则和数据结构都要动态下发的场景。
//
// 运行后依次演示：全命中场景 → 越界规则被引擎静默跳过（错误仅日志可见）→ 安全访问器兜底。
package main

import (
	"fmt"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	"github.com/hyperjumptech/grule-rule-engine/engine"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
	"go.uber.org/zap"
)

// ── Fact 定义 ────────────────────────────────────────────────────────────────

// Membership 会员信息：作为链式调用中间对象（Customer.Membership().Level）
type Membership struct {
	Name  string
	Level int
}

// HistOrder 历史订单：链式调用示例 Customer.LastOrder().Amount
type HistOrder struct {
	ID     string
	Amount float64
}

// Customer 演示「方法返回对象 → 继续取字段」的链式调用
type Customer struct {
	Name   string
	Level  int
	Orders []*HistOrder
}

// LastOrder 返回最近一笔订单；空历史返回零值订单——这本身就是一种边界保护：
// 规则里 Customer.LastOrder().Amount 永远不会因为空切片而 panic。
func (c *Customer) LastOrder() *HistOrder {
	if len(c.Orders) == 0 {
		return &HistOrder{}
	}
	return c.Orders[len(c.Orders)-1]
}

// Membership 按等级构造会员对象，供规则链式取 .Level / .Name
func (c *Customer) Membership() *Membership {
	name := "normal"
	if c.Level >= 5 {
		name = "diamond"
	}
	return &Membership{Name: name, Level: c.Level}
}

// SubFact 嵌套结构：数组里套数组，演示多级下标
type SubFact struct {
	SubFacts   []*SubFact
	AnIntArray []int
}

// Fact 数组 / map / 嵌套结构的宿主
type Fact struct {
	SubFacts []*SubFact
	SubMaps  map[string]*SubFact
}

// SubInt 安全访问 SubFacts[i].SubFacts[j].AnIntArray[k]：任何一级越界/为 nil 都返回 0，
// 规则里用 Fact.SubInt(1,2,12) 代替裸下标，数据缺失时条件安静地不成立而不是报错。
// 参数用 int64：GRL 里的整型字面量按 int64 传参，写成 int 会因类型不匹配求值失败。
func (f *Fact) SubInt(i, j, k int64) int64 {
	if f == nil || i < 0 || i >= int64(len(f.SubFacts)) || f.SubFacts[i] == nil {
		return 0
	}
	s := f.SubFacts[i]
	if j < 0 || j >= int64(len(s.SubFacts)) || s.SubFacts[j] == nil {
		return 0
	}
	arr := s.SubFacts[j].AnIntArray
	if k < 0 || k >= int64(len(arr)) {
		return 0
	}
	return int64(arr[k])
}

// MapInt 安全访问 SubMaps[key].AnIntArray[k]：key 不存在 / 数组越界返回 0
func (f *Fact) MapInt(key string, k int64) int64 {
	if f == nil {
		return 0
	}
	s, ok := f.SubMaps[key]
	if !ok || s == nil || k < 0 || k >= int64(len(s.AnIntArray)) {
		return 0
	}
	return int64(s.AnIntArray[k])
}

// Output 输出 Fact（黑板）：规则命中后往 Notes 里追加标记
type Output struct {
	Notes string
}

// ── 规则 ─────────────────────────────────────────────────────────────────────

// ruleChained 链式调用：方法返回对象再取字段，两级链均可继续展开
const ruleChained = `
rule ChainedCall "链式调用 Fact.Func().Field" salience 100 {
    when
        Customer.LastOrder().Amount > 1000 &&
        Customer.Membership().Level >= 5 &&
        Customer.Membership().Name == "diamond"
    then
        Out.Notes = Out.Notes + "[chained:钻石会员大额复购] ";
        Retract("ChainedCall");
}`

// ruleRawIndex 裸下标访问：语法上完全支持，但任何一级长度不够都会直接 panic
const ruleRawIndex = `
rule RawIndex "数组/map/嵌套裸下标访问（越界会 panic）" salience 90 {
    when
        Fact.SubFacts[1].SubFacts[2].AnIntArray[12] > 100 &&
        Fact.SubMaps["Key"].AnIntArray[0] == 1000
    then
        Out.Notes = Out.Notes + "[raw-index:深层命中] ";
        Retract("RawIndex");
}`

// ruleSafeIndex 生产建议写法：Fact 层安全访问器代替裸下标，数据缺失只是不命中
const ruleSafeIndex = `
rule SafeIndex "Fact 层边界保护的安全访问" salience 80 {
    when
        Fact.SubInt(1, 2, 12) > 100 &&
        Fact.MapInt("Key", 0) == 1000
    then
        Out.Notes = Out.Notes + "[safe-index:深层命中] ";
        Retract("SafeIndex");
}`

// ruleJSON JSON Fact：J 由 AddJSON 注入，字段/数组/嵌套对象直接点出来，无需 Go struct
const ruleJSON = `
rule JSONFact "JSON 定义的动态 Fact" salience 70 {
    when
        J.user.vip == true &&
        J.user.tags[0] == "loyal" &&
        J.user.stats.orders > 10 &&
        J.amount > 10000
    then
        Out.Notes = Out.Notes + "[json-fact:金牌用户] ";
        Retract("JSONFact");
}`

// ── 组装与演示 ────────────────────────────────────────────────────────────────

// buildKB 把若干段 GRL 构建成一个 KnowledgeBase 实例
func buildKB(name string, grls ...string) (*ast.KnowledgeBase, error) {
	lib := ast.NewKnowledgeLibrary()
	rb := builder.NewRuleBuilder(lib)
	for i, g := range grls {
		res := pkg.NewBytesResource([]byte(g))
		if err := rb.BuildRuleFromResource(name, "1.0.0", res); err != nil {
			return nil, fmt.Errorf("构建第 %d 段规则失败: %w", i+1, err)
		}
	}
	return lib.NewKnowledgeBaseInstance(name, "1.0.0")
}

// goodFact 构造满足 SubFacts[1].SubFacts[2].AnIntArray[12] 与 SubMaps["Key"] 的完整数据
func goodFact() *Fact {
	deep := &SubFact{AnIntArray: make([]int, 13)}
	deep.AnIntArray[12] = 150 // > 100

	return &Fact{
		SubFacts: []*SubFact{
			{},                                   // [0] 占位
			{SubFacts: []*SubFact{{}, {}, deep}}, // [1].SubFacts[2] = deep
		},
		SubMaps: map[string]*SubFact{
			"Key": {AnIntArray: []int{1000}},
		},
	}
}

// shortFact 故意缺数据：SubFacts 只有 1 个元素，SubMaps 缺 "Key"
func shortFact() *Fact {
	return &Fact{
		SubFacts: []*SubFact{{}},
		SubMaps:  map[string]*SubFact{},
	}
}

var customer = &Customer{
	Name:  "张三",
	Level: 5,
	Orders: []*HistOrder{
		{ID: "O-1", Amount: 300},
		{ID: "O-2", Amount: 6800}, // LastOrder
	},
}

var userJSON = []byte(`{
	"user":   {"name": "张三", "vip": true, "tags": ["loyal", "early-bird"], "stats": {"orders": 42}},
	"amount": 12000
}`)

// run 执行一次推理。越界 panic 由 grule 引擎自己 recover 成规则级错误并跳过，
// 不会传导到这里——想看到错误必须给 ast.SetLogger 注入日志器。
func run(kb *ast.KnowledgeBase, fact *Fact) (out *Output, err error) {
	dctx := ast.NewDataContext()
	out = &Output{}
	if err = dctx.Add("Customer", customer); err != nil {
		return nil, err
	}
	if err = dctx.Add("Fact", fact); err != nil {
		return nil, err
	}
	if err = dctx.AddJSON("J", userJSON); err != nil { // ★ JSON 直接成为 Fact
		return nil, err
	}
	if err = dctx.Add("Out", out); err != nil {
		return nil, err
	}
	err = engine.NewGruleEngine().Execute(dctx, kb)
	return out, err
}

func main() {
	// error 级 logger：只让 grule 打出「规则求值失败」这类错误，看清被静默跳过的规则
	logCfg := zap.NewDevelopmentConfig()
	logCfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	logger, _ := logCfg.Build()
	ast.SetLogger(logger)

	kbAll, err := buildKB("advanced-all", ruleChained, ruleRawIndex, ruleSafeIndex, ruleJSON)
	if err != nil {
		panic(err)
	}
	kbSafe, err := buildKB("advanced-safe", ruleChained, ruleSafeIndex, ruleJSON)
	if err != nil {
		panic(err)
	}

	fmt.Println("== 场景一：数据完整，四条规则全命中（链式 / 裸下标 / 安全访问 / JSON）==")
	if out, err := run(kbAll, goodFact()); err != nil {
		fmt.Println("  执行失败:", err)
	} else {
		fmt.Println("  Out.Notes =", out.Notes)
	}

	fmt.Println("== 场景二：数据缺失 + 裸下标规则 → 引擎 recover 越界 panic，规则被静默跳过 ==")
	fmt.Println("   （注意下面 grule 打出的 ERROR：RawIndex 求值失败；Execute 本身不报错）")
	if out, err := run(kbAll, shortFact()); err != nil {
		fmt.Println("  执行失败:", err)
	} else {
		fmt.Println("  Out.Notes =", out.Notes, "← 没有 [raw-index]，深层规则悄悄失效了")
	}

	fmt.Println("== 场景三：同样缺失的数据 + 安全访问器 → 干净执行（无 ERROR），深层规则安静不命中 ==")
	if out, err := run(kbSafe, shortFact()); err != nil {
		fmt.Println("  执行失败:", err)
	} else {
		fmt.Println("  Out.Notes =", out.Notes)
	}
}

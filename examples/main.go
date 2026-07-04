// grule-rule-engine 学习示例：订单折扣规则（入门） + 进阶特性（链式调用 / 深层访问 / JSON Fact）
// ============================ 整体调用关系 ============================
//
//	main()
//	 ├─ ast.SetLogger(zap)                    注入日志器（GRL 里 Log()/LogFormat() 的输出通道）
//	 ├─ 构造 *model.Order                     业务事实（fact）
//	 ├─ ast.NewDataContext()                  事实容器
//	 │    └─ dataContext.Add("Order", order)  注册事实，"Order" 即 GRL 里引用的名字
//	 ├─ builder.NewRuleBuilder(...)           规则构建器
//	 │    └─ BuildRuleFromResource(...)       解析 rules/tutorial/order.grl → AST，存入 KnowledgeLibrary
//	 ├─ knowledgeLibrary.NewKnowledgeBaseInstance(...)
//	 │                                        从库中取出一份可执行的规则集实例（KnowledgeBase）
//	 ├─ engine.NewGruleEngine()               推理引擎（默认 MaxCycle=5000）
//	 │    └─ eng.Execute(dataContext, knowledgeBase)
//	 │         │  ★ 核心：前向推理循环，每一轮称为一个 cycle
//	 │         └─ for {  // 伪代码
//	 │              ① 评估阶段：遍历所有规则，对每条规则的 when 求值
//	 │                 - 求值 Order.VIP / Order.Amount：反射读字段
//	 │                 - 求值 Order.IsBigOrder()：反射调方法（model/order.go）
//	 │                 - when == true 的规则进入候选集（冲突集）
//	 │                 - 回调 listener.EvaluateRuleEntry(..., candidate)
//	 │                   （遍历顺序来自内部 map，不稳定，与优先级无关）
//	 │              ② 候选集为空 → 循环结束，Execute 返回
//	 │              ③ 冲突消解：按 salience 降序，只挑最高的一条
//	 │              ④ 执行其 then 部分（修改事实 / Retract 自己）
//	 │                 - 回调 listener.ExecuteRuleEntry(...)
//	 │              ⑤ 事实已被修改，回到 ① 重新评估（下一个 cycle）
//	 │            }
//	 └─ order.Calc()                          规则跑完后的普通业务代码，算最终价
//
// ============================ 本例的实际执行轨迹 =======================
// 三个 Fact：
//
//	Order    Amount=12000, VIP=true, Discount=1.0, Freight=20
//	Customer 等级3, 标签[loyal,student], 注册400天(老客), 地址JP
//	Merchant 评分4.7, 退款率0.03, 经营5年 → IsTrusted()=true, IsHighRisk()=false
//
// salience 分层：风控 300~289 > 折扣 101~100 > 运费 93~92
//
//	cycle 1: 执行 ForeignAddressRisk(289)：地址JP≠CN → RiskScore+10
//	         （RejectHighRiskCombo 因老客未成候选；RefundRateRisk 因退款率低未成候选）
//	cycle 2: 执行 VipDiscount(101)：Discount=0.9
//	cycle 3: 执行 LoyalCustomerExtraDiscount(100)：老客+可信商户 → Discount=0.9*0.95=0.855
//	         （链式推理：读到的是 cycle 2 改过的 Discount）
//	cycle 4: 执行 BigOrder(93)：Freight=0
//	cycle 5: 执行 FreeFreight(92)：Freight=0（重复动作）
//	cycle 6: 候选集为空 → 结束
//
//	最终价 = 12000 * 0.855 + 0 = 10260
//
// 试着改事实观察分支：RegisterDays=10 且 Blacklisted=true → RejectHighRiskCombo
// 拒单，Order.Rejected=true 让所有折扣规则的闸门条件失效，直接输出拒单原因。
//
// 注意：salience 只决定"候选集中谁被执行"，不决定"谁先被评估"；
// NormalDiscount 因 when 为 false（VIP==true）从未进入候选集。
// https://raw.githubusercontent.com/hyperjumptech/grule-rule-engine/master/examples/TutorialExample_test.go
// https://github.com/hyperjumptech/grule-rule-engine/tree/master/examples
// https://github.com/hyperjumptech/grule-rule-engine/blob/master/docs/en/Tutorial_en.md
//
// ============================ 进阶特性（第二部分） =====================
//
//  1. 链式调用：Fact.Func().Field —— 方法返回对象后继续取字段/再调方法；
//  2. 数组 / map / 嵌套结构访问：Fact.SubFacts[1].SubFacts[2].AnIntArray[12]、
//     Fact.SubMaps["Key"].AnIntArray[0]——注意：裸下标越界会在求值时 panic；
//     当前 grule(v1.20.4) 引擎会 recover 并把它转成该条规则的评估错误、静默跳过
//     （进程不崩、Execute 不报错，只有注入 logger 才看得到），规则等于悄悄失效——
//     所以生产环境必须在 Fact 层做边界保护（见 SubInt/MapInt 安全访问器）；
//     另外 GRL 整型字面量按 int64 传参，Fact 方法参数要声明成 int64；
//  3. JSON 直接定义 Fact（DataContext.AddJSON）：无需预定义 Go struct，
//     适合规则和数据结构都要动态下发的场景。
//
// 运行方式：仓库根目录 make tutorial（规则文件在 rules/tutorial/ 下；该子目录
// 不会被服务的 FileRepository 加载——它只扫 rules/*.grl 一级，生产规则不受影响）。
package main

import (
	"context"
	"fmt"
	"tcg-ai-engine/examples/tutorial/model"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	"github.com/hyperjumptech/grule-rule-engine/engine"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
	"go.uber.org/zap"
)

// traceListener 引擎监听器：实现 engine.GruleEngineListener 接口，
// 由 Execute() 在推理循环的固定时机回调（见文件头 ①④ 两步），
// 用于观察每个 cycle 里"谁被评估为候选、谁最终被执行"。
type traceListener struct{}

// BeginCycle 每个 cycle 开始（评估阶段之前）被调用一次。
func (t *traceListener) BeginCycle(ctx context.Context, cycle uint64) {
	fmt.Printf("--- cycle %d ---\n", cycle)
}

// EvaluateRuleEntry 评估阶段每评估完一条规则的 when 就被调用一次；
// candidate=true 表示 when 成立、该规则已进入冲突集。
// 回调顺序 = 引擎内部 map 的遍历顺序，每轮可能不同，与 salience 无关。
func (t *traceListener) EvaluateRuleEntry(ctx context.Context, cycle uint64, entry *ast.RuleEntry, candidate bool) {
	if candidate {
		fmt.Printf("  候选: %s\n", entry.RuleName)
	}
}

// ExecuteRuleEntry 冲突消解选出唯一赢家、即将执行其 then 时被调用。
// 每个 cycle 至多回调一次——一轮只执行一条规则。
func (t *traceListener) ExecuteRuleEntry(ctx context.Context, cycle uint64, entry *ast.RuleEntry) {
	fmt.Printf("  执行: %s\n", entry.RuleName)
}

// ══════════════════════════ 第一部分：入门教程 ══════════════════════════

func runTutorial() {
	// 创建订单——这就是推理引擎操作的"事实"（fact）。/创建 Fact（事实/数据）
	// 规则的 when 读它的字段/方法，then 改它的字段，全部通过反射完成，
	// 所以必须传指针，否则 then 里的赋值改不到原对象。
	order := &model.Order{
		Amount:   12000,
		VIP:      true,
		Discount: 1.0,
		Freight:  20,
		// RiskScore/Rejected/RejectReason 零值起步，由 risk.grl 写入
	}

	// 客户 Fact：带嵌套结构（Address）、切片（Tags）、带参方法（HasTag）
	customer := &model.Customer{
		ID:           "C1001",
		Name:         "张三",
		Level:        3,
		Points:       8600,
		Tags:         []string{"loyal", "student"},
		Address:      model.Address{Country: "JP", City: "Tokyo"}, // 非 CN → 触发海外地址风险分
		RegisterDays: 400,                                         // 老客 → 不触发拒单规则
	}

	// 商户 Fact：风险判断逻辑封装在 IsHighRisk()/IsTrusted() 方法里
	merchant := &model.Merchant{
		ID:          "M2001",
		Name:        "旗舰数码店",
		Category:    "electronics",
		Rating:      4.7,
		RefundRate:  0.03, // 低退款率 → IsTrusted() == true
		YearsActive: 5,
		Blacklisted: false,
	}

	// 创建DataContext：事实容器, 并添加Fact
	// Add 的第一个参数就是 GRL 文件里引用的名字——名字对不上规则就找不到对象。
	// 多个 Fact 各自注册，规则里可以在同一个 when 里跨 Fact 组合条件。
	dataContext := ast.NewDataContext()
	dataContext.Add("Order", order)
	dataContext.Add("Customer", customer)
	dataContext.Add("Merchant", merchant)
	// 创建Knowledge Library：已编译规则集（AST）的仓库，
	// 以 (name, version) 为键，可存多套规则、可被多个引擎共享。
	knowledgeLibrary := ast.NewKnowledgeLibrary()
	// 创建Rule Builder：负责把 GRL 文本解析成 AST 并注册进上面的 Library。
	ruleBuilder := builder.NewRuleBuilder(knowledgeLibrary)

	// 加载GRL文件。除文件外还有 NewBytesResource / NewURLResource 等来源。
	// 多个文件用 BuildRuleFromResources 一次性合并进同一个 KnowledgeBase；
	// 也可用 bundle 按通配符批量加载：
	// bundle := pkg.NewFileResourceBundle("rules/tutorial", "rules/tutorial/**/*.grl")
	// err = ruleBuilder.BuildRulesFromBundle("OrderRules", "0.0.1", bundle)
	// 注意路径相对仓库根目录（make tutorial 即在根目录运行）。
	resources := []pkg.Resource{
		pkg.NewFileResource("rules/tutorial/order.grl"), // 折扣/运费规则
		pkg.NewFileResource("rules/tutorial/risk.grl"),  // 风控规则（跨 Fact）
		// pkg.NewGITResourceBundle
	}

	// 规则文件绑定：解析所有 GRL，以 ("OrderRules", "0.0.1") 为键存入 Library。
	// GRL 语法错误（如跨文件的规则名重复）在这一步就会报出来，而不是等到执行时。
	err := ruleBuilder.BuildRuleFromResources("OrderRules", "0.0.1", resources)
	if err != nil {
		panic(err)
	}

	// 创建KnowledgeBase：从 Library 按 (name, version) 取出一份规则集实例。
	// 实例持有执行期状态（如 Retract 的撤回标记），并发执行时各协程应各取一份实例。
	knowledgeBase, err := knowledgeLibrary.NewKnowledgeBaseInstance("OrderRules", "0.0.1")
	if err != nil {
		panic(err)
	}

	// 创建推理引擎。引擎本身无状态，MaxCycle 默认 5000：
	// 若规则不会"熄火"（不 Retract、条件也不变 false），跑满上限会返回错误。
	eng := engine.NewGruleEngine()

	// 挂载监听器，跟踪每个cycle的规则评估与执行
	eng.Listeners = []engine.GruleEngineListener{&traceListener{}}

	// 执行规则：进入文件头注释描述的 cycle 循环，
	// 直到某一轮候选集为空才返回。返回后 order 里的
	// Discount/Freight 已被规则改写（本例：0.9 / 0）。
	err = eng.Execute(dataContext, knowledgeBase)
	if err != nil {
		panic(err)
	}

	// 执行普通业务代码：规则只负责"定参数"（折扣、运费），
	// 最终价的计算留在 Go 代码里，体现"规则与计算分离"。
	order.Calc()

	// 输出
	fmt.Println("客户：", customer.Name, "等级:", customer.Level, "标签:", customer.Tags)
	fmt.Println("商户：", merchant.Name, "评分:", merchant.Rating, "可信:", merchant.IsTrusted())
	fmt.Println("风险分：", order.RiskScore)
	if order.Rejected {
		fmt.Println("订单被拒：", order.RejectReason)
		return
	}
	fmt.Println("原价：", order.Amount)
	fmt.Println("VIP：", order.VIP)
	fmt.Println("折扣：", order.Discount)
	fmt.Println("运费：", order.Freight)
	fmt.Println("最终价格：", order.FinalAmount)
}

// ══════════════════════════ 第二部分：进阶特性 ══════════════════════════
//
// 链式调用 / 数组·map·嵌套结构访问（含越界防护）/ JSON Fact。
// 本部分自带 Fact 定义与内联 GRL，与第一部分的 model 包无关。

// Membership 会员信息：作为链式调用中间对象（Chain.Membership().Level）
type Membership struct {
	Name  string
	Level int
}

// HistOrder 历史订单：链式调用示例 Chain.LastOrder().Amount
type HistOrder struct {
	ID     string
	Amount float64
}

// ChainCustomer 演示「方法返回对象 → 继续取字段」的链式调用
type ChainCustomer struct {
	Name   string
	Level  int
	Orders []*HistOrder
}

// LastOrder 返回最近一笔订单；空历史返回零值订单——这本身就是一种边界保护：
// 规则里 Chain.LastOrder().Amount 永远不会因为空切片而 panic。
func (c *ChainCustomer) LastOrder() *HistOrder {
	if len(c.Orders) == 0 {
		return &HistOrder{}
	}
	return c.Orders[len(c.Orders)-1]
}

// Membership 按等级构造会员对象，供规则链式取 .Level / .Name
func (c *ChainCustomer) Membership() *Membership {
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

// ruleChained 链式调用：方法返回对象再取字段，两级链均可继续展开
const ruleChained = `
rule ChainedCall "链式调用 Fact.Func().Field" salience 100 {
    when
        Chain.LastOrder().Amount > 1000 &&
        Chain.Membership().Level >= 5 &&
        Chain.Membership().Name == "diamond"
    then
        Out.Notes = Out.Notes + "[chained:钻石会员大额复购] ";
        Retract("ChainedCall");
}`

// ruleRawIndex 裸下标访问：语法上完全支持，但任何一级长度不够都会在求值时 panic
// （v1.20.4 引擎 recover 后该规则静默跳过，错误仅日志可见）
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

// buildKB 把若干段内联 GRL 构建成一个 KnowledgeBase 实例
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

var chainCustomer = &ChainCustomer{
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

// runAdvancedOnce 执行一次进阶推理。越界 panic 由 grule 引擎自己 recover 成
// 规则级错误并跳过，不会传导到这里——想看到错误必须给 ast.SetLogger 注入日志器。
func runAdvancedOnce(kb *ast.KnowledgeBase, fact *Fact) (out *Output, err error) {
	dctx := ast.NewDataContext()
	out = &Output{}
	if err = dctx.Add("Chain", chainCustomer); err != nil {
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

func runAdvanced() {
	kbAll, err := buildKB("advanced-all", ruleChained, ruleRawIndex, ruleSafeIndex, ruleJSON)
	if err != nil {
		panic(err)
	}
	kbSafe, err := buildKB("advanced-safe", ruleChained, ruleSafeIndex, ruleJSON)
	if err != nil {
		panic(err)
	}

	fmt.Println("== 场景一：数据完整，四条规则全命中（链式 / 裸下标 / 安全访问 / JSON）==")
	if out, err := runAdvancedOnce(kbAll, goodFact()); err != nil {
		fmt.Println("  执行失败:", err)
	} else {
		fmt.Println("  Out.Notes =", out.Notes)
	}

	fmt.Println("== 场景二：数据缺失 + 裸下标规则 → 引擎 recover 越界 panic，规则被静默跳过 ==")
	fmt.Println("   （注意 grule 打出的 ERROR：RawIndex 求值失败；Execute 本身不报错）")
	if out, err := runAdvancedOnce(kbAll, shortFact()); err != nil {
		fmt.Println("  执行失败:", err)
	} else {
		fmt.Println("  Out.Notes =", out.Notes, "← 没有 [raw-index]，深层规则悄悄失效了")
	}

	fmt.Println("== 场景三：同样缺失的数据 + 安全访问器 → 干净执行（无 ERROR），深层规则安静不命中 ==")
	if out, err := runAdvancedOnce(kbSafe, shortFact()); err != nil {
		fmt.Println("  执行失败:", err)
	} else {
		fmt.Println("  Out.Notes =", out.Notes)
	}
}

// ══════════════════════════ 入口 ══════════════════════════

func main() {
	// 注入真实logger（默认是Noop，规则里的 Log()/LogFormat() 不会有任何输出）
	// 注意：GRL内置Log()用的是ast包的GrlLogger，必须调ast.SetLogger才生效；
	// logger.SetLogger只改logger.Log，各包init时已缓存Noop副本，改了也没人用
	// ast.SetLogger 内部按类型分发，原生支持 *zap.Logger / *logrus.Logger / *zerolog.Logger
	// 开发模式配置（人类可读格式），但级别提到 Info——
	// Debug 级别下 grule 会打印海量 AST 构建/求值内部日志，淹没规则输出
	zapCfg := zap.NewDevelopmentConfig()
	zapCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	zapLog, err := zapCfg.Build()
	if err != nil {
		panic(err)
	}
	defer zapLog.Sync()
	ast.SetLogger(zapLog)

	fmt.Println("════════════ 第一部分：入门教程（订单折扣规则） ════════════")
	runTutorial()

	fmt.Println()
	fmt.Println("════════════ 第二部分：进阶特性（链式调用 / 深层访问 / JSON Fact） ════════════")
	runAdvanced()
}

// grule-rule-engine 学习示例：订单折扣规则
// ============================ 整体调用关系 ============================
//
//	main()
//	 ├─ ast.SetLogger(logrus.New())          注入日志器（GRL 里 Log()/LogFormat() 的输出通道）
//	 ├─ 构造 *model.Order                     业务事实（fact）
//	 ├─ ast.NewDataContext()                  事实容器
//	 │    └─ dataContext.Add("Order", order)  注册事实，"Order" 即 GRL 里引用的名字
//	 ├─ builder.NewRuleBuilder(...)           规则构建器
//	 │    └─ BuildRuleFromResource(...)       解析 rules/order.grl → AST，存入 KnowledgeLibrary
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
	// bundle := pkg.NewFileResourceBundle("rules", "rules/**/*.grl")
	// err = ruleBuilder.BuildRulesFromBundle("OrderRules", "0.0.1", bundle)
	resources := []pkg.Resource{
		pkg.NewFileResource("examples/tutorial/rules/order.grl"), // 折扣/运费规则
		pkg.NewFileResource("examples/tutorial/rules/risk.grl"),  // 风控规则（跨 Fact）
	}

	// 规则文件绑定：解析所有 GRL，以 ("OrderRules", "0.0.1") 为键存入 Library。
	// GRL 语法错误（如跨文件的规则名重复）在这一步就会报出来，而不是等到执行时。
	err = ruleBuilder.BuildRuleFromResources("OrderRules", "0.0.1", resources)
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

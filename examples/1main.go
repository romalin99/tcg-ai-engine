// grule-rule-engine 入门示例 —— 风控打分场景(完整注解版)
//
// 演示 grule 的五个核心机制:
//  1. Fact 注入        : DataContext.Add 把 Go 结构体指针交给引擎
//  2. GRL 规则语法      : rule 名称 "描述" salience 优先级 { when 条件 then 动作 }
//  3. salience 冲突消解 : 每个 cycle 只执行"已匹配规则中 salience 最高的一条"
//  4. Retract          : when 不能自我证伪的规则, 执行后必须自我撤出
//  5. Complete()       : 全局短路, 立即终止本次推理 (对应"分数≥阈值→直接决策")
//
// 运行: go run ./examples/1main.go
// （本文件带 ignore 构建标签: 与 main.go 同包同 main() 会冲突, 不参与包构建;
//   go run 显式指定文件时会忽略构建标签, 所以上面的命令照常可用）

//go:build ignore

package main

import (
	"fmt"
	"log"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	"github.com/hyperjumptech/grule-rule-engine/engine"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
)

// ============================================================
// 事实层 (Fact): 引擎通过反射读写的 Go 对象
//
// 注入约定:
//   dctx.Add("Txn", &Transaction{...})   → GRL 中以 Txn.xxx 引用
//   dctx.Add("Result", &RiskResult{...}) → GRL 中以 Result.xxx 引用
//
// 两条硬性约束(违反不报编译错, 只在运行期出问题):
//   1. 必须传指针 —— 传值时规则改的是副本, Execute 返回后 Go 侧拿不到结果
//   2. 只有导出(大写开头)字段/方法对 GRL 可见 —— 反射机制决定
//
// json tag 与 grule 无关(grule 按字段名反射), 仅用于 HTTP 层收发特征
// ============================================================

// Transaction 待评估交易 —— 只读输入事实
//
// 设计约定: 对规则只读, 所有字段都是数据预处理/特征工程阶段算好的
// "现成特征", 引擎内不做特征加工。
// 维护提醒: 字段名被 GRL 按字符串反射匹配, 重命名字段不会产生编译错,
// 只会在运行期报错 —— 改结构体时必须同步排查规则文本。
type Transaction struct {
	// Amount 单笔交易金额(单位: 元)
	// 被引用: PAY-001 新设备大额支付
	Amount float64 `json:"amount"`

	// IsNewDevice 是否新设备(设备指纹判定: 首次出现/新注册)
	// 被引用: PAY-001
	IsNewDevice bool `json:"is_new_device"`

	// Inflow24h 近24小时资金流入总额(单位: 元)
	// 被引用: AML-001 大额快进快出
	Inflow24h float64 `json:"inflow_24h"`

	// OutflowRatio 近24小时 流出/流入 比值, 取值 [0, 1+]
	// 越接近 1 表示"钱来了马上走", 是典型过账特征
	// 被引用: AML-001
	OutflowRatio float64 `json:"outflow_ratio"`

	// NightRatio 夜间(00:00-06:00)交易笔数占比, 取值 [0, 1]
	// 被引用: AML-002 夜间高频交易
	NightRatio float64 `json:"night_ratio"`

	// TxnCount24h 近24小时交易笔数
	// 类型提醒: int64, GRL 中与整数字面量(50)比较;
	// float64 字段则必须与小数字面量(50.0)搭配, 避免反射类型不匹配
	// 被引用: AML-002
	TxnCount24h int64 `json:"txn_count_24h"`
}

// RiskResult 决策结果 —— 可写输出事实(规则回写的唯一对象)
//
// 写入方式约定(grule 工作内存缓存机制决定, 是本示例最重要的约定):
//
//	· Score / Decision 出现在多条规则的 when 中
//	  → GRL 内必须用【赋值语句】修改(自动使缓存失效),
//	    或方法调用后显式补 Changed("Result.Score")
//	· HitRules 不出现在任何 when 中
//	  → 可安全用方法 Hit() 追加, 无需通知工作内存
type RiskResult struct {
	// Score 累计风险分, 由打分层规则逐条累加, 初值 0
	// 参与 when: ThresholdBlock / FinalManual / FinalPass
	// 修改方式: 仅限 GRL 赋值语句 Result.Score = Result.Score + x
	Score float64 `json:"score"`

	// Threshold 直接决策阈值(短路线)
	// 刻意做成"每次注入的数据"而非硬编码进 GRL:
	// 调阈值只改 Go 侧注入值, 规则文本零改动(对应优化闭环的"调整阈值")
	Threshold float64 `json:"threshold"`

	// Decision 最终决策, 取值: "拦截/拒绝" | "人工审核" | "通过"
	// 空串 "" 表示尚未决策 —— 兜底层规则以此作守卫,
	// 配合分数轴的互斥划分, 保证 Decision 恰好被赋值一次
	// 修改方式: 仅限 GRL 赋值语句 Result.Decision = "..."
	Decision string `json:"decision"`

	// HitRules 命中规则清单(证据链雏形)
	// 生产建议升级为结构体切片: []HitRecord{规则ID, 分值, 证据, 时间}
	HitRules []string `json:"hit_rules"`
}

// AddScore 累加风险分。
// ⚠ 警告: 在 GRL 中调用本方法不会通知工作内存(Score 的缓存不失效),
// 会导致依赖 Score 的规则读到旧值。规则内改分请用赋值语句;
// 本方法仅保留给 Go 侧使用(见反面教材演示)。
func (r *RiskResult) AddScore(s float64) { r.Score += s }

// Hit 追加一条命中记录(证据)。
// HitRules 不被任何 when 引用, GRL 中可安全调用。
func (r *RiskResult) Hit(name string) { r.HitRules = append(r.HitRules, name) }

// Decide 写入最终决策。
// ⚠ 同 AddScore 的警告: Decision 被 when 引用, GRL 内请用赋值语句;
// 本方法保留给 Go 侧兜底(如引擎执行失败后的降级决策)。
func (r *RiskResult) Decide(d string) { r.Decision = d }

// ============================================================
// 规则层 (GRL)
// 生产环境放独立 .grl 文件, 用 pkg.NewFileResource 加载;
// 这里为单文件演示用字符串 + NewBytesResource
// ============================================================

const grl = `
// ╔════════════════════════════════════════════════════════════╗
// ║ salience 频段规划(三层互不重叠, 数字大者先执行)              ║
// ║                                                            ║
// ║   1000    抢占层: 阈值短路, 条件一旦成立即碾压一切            ║
// ║   100~80  打分层: 业务规则, 洗钱域(100/90) > 支付域(80)      ║
// ║   2~1     兜底层: 聚合决策, 打分层全部出清后才轮到            ║
// ║                                                            ║
// ║ 兜底层三条决策对分数轴的划分【完整且互斥】:                    ║
// ║   [0,40) → 通过   [40,阈值) → 人工   [阈值,∞) → 拦截        ║
// ║ 配合 Decision=="" 守卫, 保证决策恰好落一次, 不漏判不互踩      ║
// ╚════════════════════════════════════════════════════════════╝

// ────────────────────────────────────────────────────────────
// 规则编号: AML-001                    风险域: 洗钱(AML)
// 业务含义: 大额资金快进快出 —— 24h 大额流入且九成以上随即流出,
//           典型的过账/资金通道行为
// 触发条件: 24h流入 > 50万元  且  流出占比 > 0.9
// 风险分值: +80(高危)
// 优先级  : salience 100 —— 打分层最高, 洗钱域最先评估
// 执行动作: 累分(赋值语句,自动失效缓存) → 记证据(方法,安全)
//           → Retract 自我撤出
// Retract 原因: when 只依赖只读的 Txn, 执行后条件仍为真,
//               不撤出会在后续 cycle 无限重触发
// ────────────────────────────────────────────────────────────
rule AMLFastInOut "洗钱-大额快进快出" salience 100 {
    when
        Txn.Inflow24h > 500000.0 && Txn.OutflowRatio > 0.9
    then
        Result.Score = Result.Score + 80.0;
        Result.Hit("AML-001 大额快进快出");
        Retract("AMLFastInOut");
}

// ────────────────────────────────────────────────────────────
// 规则编号: AML-002                    风险域: 洗钱(AML)
// 业务含义: 夜间高频交易 —— 凌晨时段集中放量, 规避人工盯盘的
//           常见操作时间窗
// 触发条件: 夜间交易占比 > 0.7  且  24h笔数 > 50
// 风险分值: +45(中高危)
// 优先级  : salience 90 —— 洗钱域次序第二
// 执行动作: 同 AML-001 三段式
// ────────────────────────────────────────────────────────────
rule AMLNightBurst "洗钱-夜间高频交易" salience 90 {
    when
        Txn.NightRatio > 0.7 && Txn.TxnCount24h > 50
    then
        Result.Score = Result.Score + 45.0;
        Result.Hit("AML-002 夜间高频交易");
        Retract("AMLNightBurst");
}

// ────────────────────────────────────────────────────────────
// 规则编号: PAY-001                    风险域: 支付
// 业务含义: 新设备大额支付 —— 首次出现的设备直接发起大额交易,
//           盗号/盗卡的高相关信号
// 触发条件: 新设备  且  单笔金额 > 5万元
// 风险分值: +40(中危)
// 优先级  : salience 80 —— 支付域排在洗钱域之后
// 执行动作: 同上三段式
// ────────────────────────────────────────────────────────────
rule PayNewDeviceLarge "支付-新设备大额" salience 80 {
    when
        Txn.IsNewDevice == true && Txn.Amount > 50000.0
    then
        Result.Score = Result.Score + 40.0;
        Result.Hit("PAY-001 新设备大额支付");
        Retract("PayNewDeviceLarge");
}

// ────────────────────────────────────────────────────────────
// 规则编号: CTRL-001                   层级: 抢占层(短路)
// 业务含义: 累计分数触达阈值 → 不再等剩余规则, 立即拦截
//           (对应架构图"当前风险分数 ≥ 阈值 → 直接决策")
// 触发条件: Score >= Threshold(阈值随请求注入, 非硬编码)
// 优先级  : salience 1000 —— 必须是全场最高:
//           条件一旦成立, 下个 cycle 它就是冲突集第一名,
//           低优先级规则连执行机会都没有 → 这才是真短路
// 执行动作: 写决策(赋值语句) → Complete() 终止整个推理
// 无需 Retract: Complete 已终止会话
// ────────────────────────────────────────────────────────────
rule ThresholdBlock "分数达到阈值立即拦截(短路)" salience 1000 {
    when
        Result.Score >= Result.Threshold
    then
        Result.Decision = "拦截/拒绝";
        Complete();
}

// ────────────────────────────────────────────────────────────
// 规则编号: CTRL-002                   层级: 兜底层(聚合决策)
// 业务含义: 分数落在灰色地带 → 转人工审核
// 触发条件: 尚未决策  且  40 ≤ Score < Threshold
// 优先级  : salience 2 —— 低于所有打分规则, 保证打分全部
//           完成后才做聚合决策
// 防重触发: 靠 Decision=="" 守卫, 赋值后 when 自我证伪,
//           无需 Retract(与打分规则的防重手法对比)
// ────────────────────────────────────────────────────────────
rule FinalManual "聚合决策-转人工" salience 2 {
    when
        Result.Decision == "" && Result.Score >= 40.0 && Result.Score < Result.Threshold
    then
        Result.Decision = "人工审核";
}

// ────────────────────────────────────────────────────────────
// 规则编号: CTRL-003                   层级: 兜底层(聚合决策)
// 业务含义: 低分 → 直接放行
// 触发条件: 尚未决策  且  Score < 40
// 优先级  : salience 1 —— 全场最低, 最后的兜底
// 防重触发: 同 CTRL-002, 赋值后自我证伪
// ────────────────────────────────────────────────────────────
rule FinalPass "聚合决策-通过" salience 1 {
    when
        Result.Decision == "" && Result.Score < 40.0
    then
        Result.Decision = "通过";
}
`

// ────────────────────────────────────────────────────────────
// 反面教材(勿模仿): 同时踩中两个坑的恒真规则
//
//	坑1: when 只依赖 Txn, then 不改变 when 引用的任何数据
//	     → 条件永真, 且无 Retract → 每个 cycle 都被重新选中
//	坑2: 用方法 AddScore 改分 → 工作内存不知情(即使 when 引用了
//	     Score 也读不到新值)
//	结局: 触发 MaxCycle 保护, 引擎返回错误
//
// ────────────────────────────────────────────────────────────
const grlInfiniteLoop = `
rule NoRetract "忘记Retract的规则" {
    when
        Txn.Amount > 0.0
    then
        Result.AddScore(1.0);
}
`

// ============================================================
// 装配层: 编译一次(Library) → 每次评估取实例(Instance)
// ============================================================

// buildKnowledgeLibrary 把 GRL 文本编译进知识库。
// 启动时执行一次; 规则热更新 = 重建 Library, 新请求取新实例,
// 跑到一半的老请求用旧实例自然收尾(天然灰度)。
func buildKnowledgeLibrary(name, version, grlText string) *ast.KnowledgeLibrary {
	lib := ast.NewKnowledgeLibrary()
	rb := builder.NewRuleBuilder(lib)
	if err := rb.BuildRuleFromResource(name, version, pkg.NewBytesResource([]byte(grlText))); err != nil {
		log.Fatalf("GRL 编译失败: %v", err)
	}
	return lib
}

// evaluate 对一笔交易执行一次完整推理。
// 关键: KnowledgeBaseInstance 内含工作内存(求值缓存+Retract状态),
// 是有状态对象 —— 并发场景每个 goroutine/每笔请求各取一个, 严禁共享。
func evaluate(lib *ast.KnowledgeLibrary, label string, txn *Transaction) {
	kb, err := lib.NewKnowledgeBaseInstance("risk", "1.0.0")
	if err != nil {
		log.Fatalf("获取知识库实例失败: %v", err)
	}

	// Threshold 在这里注入 —— 调阈值不动规则文本
	result := &RiskResult{Threshold: 100}

	// DataContext: 事实容器, key 即 GRL 中的变量名(Txn / Result)
	dctx := ast.NewDataContext()
	if err := dctx.Add("Txn", txn); err != nil {
		log.Fatal(err)
	}
	if err := dctx.Add("Result", result); err != nil {
		log.Fatal(err)
	}

	eng := engine.NewGruleEngine()
	eng.MaxCycle = 100 // 推理预算(默认 5000): 防恒真规则的最后保险
	if err := eng.Execute(dctx, kb); err != nil {
		log.Fatalf("引擎执行失败: %v", err)
	}

	fmt.Printf("── %s\n", label)
	fmt.Printf("   总分: %.0f | 决策: %s\n", result.Score, result.Decision)
	if len(result.HitRules) > 0 {
		fmt.Printf("   命中: %v\n", result.HitRules)
	}
	fmt.Println()
}

// demoInfiniteLoop 演示反面教材的实际后果:
// 恒真规则被反复选中, 触发 MaxCycle 保护, Execute 返回错误。
func demoInfiniteLoop() {
	lib := ast.NewKnowledgeLibrary()
	rb := builder.NewRuleBuilder(lib)
	if err := rb.BuildRuleFromResource("loop", "1.0.0", pkg.NewBytesResource([]byte(grlInfiniteLoop))); err != nil {
		log.Fatal(err)
	}
	kb, _ := lib.NewKnowledgeBaseInstance("loop", "1.0.0")

	dctx := ast.NewDataContext()
	dctx.Add("Txn", &Transaction{Amount: 100})
	dctx.Add("Result", &RiskResult{})

	eng := engine.NewGruleEngine()
	eng.MaxCycle = 10 // 调小以快速触发保护
	err := eng.Execute(dctx, kb)
	fmt.Printf("── 反面教材: 无 Retract 的恒真规则\n   引擎返回错误: %v\n\n", err)
}

func main() {
	lib := buildKnowledgeLibrary("risk", "1.0.0", grl)

	fmt.Println("═══ grule 风控示例 (阈值 100) ═══")

	// 场景1: 洗钱高危 → AML-001(+80) + AML-002(+45) = 125 ≥ 100
	//        → CTRL-001 抢占短路 → 拦截; 兜底层一次都没执行
	evaluate(lib, "场景1: 高危(快进快出+夜间高频)", &Transaction{
		Inflow24h: 800000, OutflowRatio: 0.95,
		NightRatio: 0.82, TxnCount24h: 120,
	})

	// 场景2: PAY-001(+40) → 未达阈值 → CTRL-002 聚合决策转人工
	//        (40 恰在边界: [40,阈值) 归人工, 边界含左端点)
	evaluate(lib, "场景2: 中风险(新设备大额)", &Transaction{
		IsNewDevice: true, Amount: 60000,
	})

	// 场景3: 零命中 → Score=0 → CTRL-003 放行, 两个 cycle 收敛
	evaluate(lib, "场景3: 正常交易", &Transaction{
		Amount: 200,
	})

	demoInfiniteLoop()
}

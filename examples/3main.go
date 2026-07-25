// grule-rule-engine 进阶示例 —— 风控打分 + 决策树/决策表/决策图/决策流(在 3main.go 基础上)
//
// 演示 grule 的九个核心机制:
//  1. Fact 注入        : DataContext.Add 把 Go 结构体指针交给引擎
//  2. GRL 规则语法      : rule 名称 "描述" salience 优先级 { when 条件 then 动作 }
//  3. salience 冲突消解 : 每个 cycle 只执行"已匹配规则中 salience 最高的一条"
//  4. Retract          : when 不能自我证伪的规则, 执行后必须自我撤出
//  5. Complete()       : 全局短路, 立即终止本次推理 (对应"分数≥阈值→直接决策")
//  6. Changed()        : 用【方法】修改被 when 引用的字段后, 手动失效工作内存缓存,
//     让规则靠"自我证伪"退出冲突集而无需 Retract(赋值语句才会自动失效;
//     见 GRADE-001 与场景2; 三个动词各用各的规则, 不混用)
//  7. Listener + zap   : 用 GruleEngineListener 逐 cycle 打印冲突集与执行次序,
//     并把 grule 内部日志统一接到 uber zap
//  8. 决策构件(第二部分): 决策表 / 决策树 / 决策图 / 决策流 四种规则组织
//     范式 —— 见文件后半"★ 第二部分"起的注解与 demoDecisionTable/Tree/Graph/Flow
//  9. 高级语法(第三部分): IN/LIKE/正则 等 SQL 式条件的 GRL 对照写法,
//     字符串内置方法 / 数组与 Map 访问 / 嵌套 fact / &&·||·! 组合 /
//     Max·Min·Sum 聚合 —— 见"★ 第三部分"注解块与 demoAdvancedSyntax
//
// 运行: go run ./examples/3main.go
// （本文件带 ignore 构建标签: 与 main.go 同包同 main() 会冲突, 不参与包构建;
//   go run 显式指定文件时会忽略构建标签, 所以上面的命令照常可用）

//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	"github.com/hyperjumptech/grule-rule-engine/engine"
	grulelogger "github.com/hyperjumptech/grule-rule-engine/logger"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ============================================================
// 观测层: zap 日志 + 引擎执行追踪监听器
//
// grule 的两个观测接入点(v1.15 源码确认):
//   A. logger.SetLogger(*zap.Logger) —— 官方提供的日志替换入口
//      ⚠ v1.15 实测限制: engine 等内部包在【包 init 时】就执行了
//        var log = logger.Log.WithFields(...) 捕获默认 logrus,
//        main() 里再 SetLogger 只替换 logger.Log 本体, 换不掉
//        已捕获的引用 → 内部日志(如 Max cycle reached)仍是 logrus 格式。
//        本文件保留该调用以演示 API; 真正的 cycle 追踪(接入点 B)全走 zap。
//   B. GruleEngine.Listeners []GruleEngineListener
//      → 引擎在推理循环的三个时机回调:
//        BeginCycle(cycle)                    每个 cycle 开始
//        EvaluateRuleEntry(cycle, rule, cand) 每条规则被求值(cand=是否进冲突集)
//        ExecuteRuleEntry(cycle, rule)        本 cycle 胜出并被执行的那【一条】
//        ⚠ 引擎原生没有 Before/After 钩子, 且 ExecuteRuleEntry 在 then 执行
//        【前】触发 —— 本文件在监听器上自建 BeforeExecuteRule / AfterExecuteRule:
//        Before = ExecuteRuleEntry 时机; After = 下一轮 BeginCycle(下一轮开始
//        即上一轮执行完毕) 补发, 最后一轮由 Execute 返回后的 Finish() 兜底
//   C. GRL 内置 Log()/LogFormat() —— 规则 then 里的打印语句,
//      走 ast 包的 GrlLogger: 必须 ast.SetLogger(zlog) 单独注入才有输出
//      (logger.SetLogger 只换 logger.Log 本体, 覆盖不到 ast 已持有的实例)
//      ⚠ LogFormat(format, 单个参数) 不是变参: 传两个值会在 then 里
//        reflect panic —— 且 then 的错误会【中止整个 Execute】(返回 error),
//        与 when 求值错误的"静默跳过该规则"行为不同, 危害更大
// ============================================================

// newDemoLogger 构造演示用 zap:
// 短时间戳、带 caller(文件:行号)、无堆栈, INFO 起打。
// 行号能直接区分日志来源: 3main.go:xx = 监听器追踪 / 快照,
// ast/BuiltInFunctions.go:xx = GRL 内置 Log()/LogFormat(),
// ast/RuleEntry.go:xx = 引擎内部(如求值失败)。
// 想看"求值了但未进冲突集"的规则, 把 Level 调成 DebugLevel 即可。
func newDemoLogger() *zap.Logger {
	cfg := zap.NewDevelopmentConfig()
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05.000")
	cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	cfg.DisableCaller = false // 打印调用位置(文件:行号)
	cfg.DisableStacktrace = true
	zlog, err := cfg.Build()
	if err != nil {
		log.Fatalf("构建 zap 失败: %v", err)
	}
	return zlog
}

// CycleTraceListener 逐 cycle 追踪器 —— 实现 engine.GruleEngineListener。
//
// 三个回调与推理循环的对应关系(也是日志的阅读顺序):
//
//	✔ 执行后(AfterExecuteRule) 规则X + 结果快照 ← 上一轮 then 落地后补发
//	── cycle N 开始
//	├ 冲突集+ 规则X (salience …)   ← 所有 when 为真的规则逐条上报
//	├ 冲突集+ 规则Y (salience …)
//	└ ▶ 执行前(BeforeExecuteRule) 规则X + 执行前快照 ← 胜出者, then 即将运行
//
// 由此能直接在日志里"看见"冲突消解: 谁匹配了、谁胜出了、谁始终没轮上。
type CycleTraceListener struct {
	zlog   *zap.Logger
	result *RiskResult // Before/After 快照的数据来源

	// pending: 已 Before、尚未 After 的胜出规则。引擎没有"then 执行完毕"的
	// 原生钩子, After 在两个时机补发: 下一轮 BeginCycle, 或 Execute 返回后的 Finish()
	pending      *ast.RuleEntry
	pendingCycle uint64
}

// BeforeExecuteRule 胜出规则即将执行(then 尚未运行)——打印执行前的结果快照
func (l *CycleTraceListener) BeforeExecuteRule(cycle uint64, entry *ast.RuleEntry) {
	l.zlog.Info("└ ▶ \t执行前", zap.Uint64("cycle", cycle), zap.String("rule", entry.RuleName), zap.String("desc", entry.RuleDescription),
		zap.Int("salience", entry.Salience), zap.Float64("score", l.result.Score), zap.String("decision", l.result.Decision))
}

// AfterExecuteRule 胜出规则的 then 已落地——打印执行后的结果快照
func (l *CycleTraceListener) AfterExecuteRule(cycle uint64, entry *ast.RuleEntry) {
	l.zlog.Info("✔ \t执行后", zap.Uint64("cycle", cycle), zap.String("rule", entry.RuleName), zap.String("desc", entry.RuleDescription),
		zap.Float64("score", l.result.Score), zap.String("decision", l.result.Decision), zap.Int64s("hits", l.result.HitIDs()))
}

// flushAfter 补发 pending 的 AfterExecuteRule
func (l *CycleTraceListener) flushAfter() {
	if l.pending != nil {
		l.AfterExecuteRule(l.pendingCycle, l.pending)
		l.pending = nil
	}
}

// Finish 在 eng.Execute 返回后调用: 补发最后一轮胜出规则的 AfterExecuteRule
// (Complete() 短路 / 冲突集耗尽 / MaxCycle 中止后都不会再有 BeginCycle)
func (l *CycleTraceListener) Finish() { l.flushAfter() }

// BeginCycle 每个推理 cycle 开始时回调（v1.20.x 起首参为 context.Context）
func (l *CycleTraceListener) BeginCycle(ctx context.Context, cycle uint64) {
	// 下一轮开始 == 上一轮胜出规则的 then 已落地 → 先补发它的 AfterExecuteRule
	l.flushAfter()
	l.zlog.Info("── cycle 开始", zap.Uint64("cycle", cycle))
}

// EvaluateRuleEntry 每条规则求值后回调; candidate=true 表示 when 为真、进入冲突集。
// 未进冲突集的记 Debug(默认不显示), 避免刷屏。
func (l *CycleTraceListener) EvaluateRuleEntry(ctx context.Context, cycle uint64, entry *ast.RuleEntry, candidate bool) {
	if candidate {
		l.zlog.Info("├ 冲突集+", zap.Uint64("cycle", cycle), zap.String("rule", entry.RuleName), zap.String("desc", entry.RuleDescription), zap.Int("salience", entry.Salience))
	} else {
		l.zlog.Debug("│ 求值未命中", zap.Uint64("cycle", cycle), zap.String("rule", entry.RuleName), zap.String("desc", entry.RuleDescription))
	}
}

// ExecuteRuleEntry 本 cycle 冲突消解的胜出者【即将】执行时回调(then 未运行)
// —— 正是 Before 语义; 同时登记 pending, 供 After 在合适时机补发。
func (l *CycleTraceListener) ExecuteRuleEntry(ctx context.Context, cycle uint64, entry *ast.RuleEntry) {
	l.BeforeExecuteRule(cycle, entry)
	l.pending, l.pendingCycle = entry, cycle
}

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
	// Amount 单笔交易金额(单位: 元) |  被引用: PAY-001 新设备大额支付
	Amount float64 `json:"amount"`
	// IsNewDevice 是否新设备(设备指纹判定: 首次出现/新注册) |  被引用: PAY-001
	IsNewDevice bool `json:"is_new_device"`
	// Inflow24h 近24小时资金流入总额(单位: 元) |  被引用: AML-001 大额快进快出
	Inflow24h float64 `json:"inflow_24h"`
	// OutflowRatio 近24小时 流出/流入 比值, 取值 [0, 1+]	 越接近 1 表示"钱来了马上走", 是典型过账特征 |  被引用: AML-001
	OutflowRatio float64 `json:"outflow_ratio"`
	// NightRatio 夜间(00:00-06:00)交易笔数占比, 取值 [0, 1] |  被引用: AML-002 夜间高频交易
	NightRatio float64 `json:"night_ratio"`
	// TxnCount24h 近24小时交易笔数	// 类型提醒: int64, GRL 中与整数字面量(50)比较;	// float64 字段则必须与小数字面量(50.0)搭配, 避免反射类型不匹配 | 被引用: AML-002
	TxnCount24h int64 `json:"txn_count_24h"`
}

// Evidence 把全部特征格式化成触发时刻的快照串, 供 HitRec 落证据。
// 收在 Go 侧的原因: GRL 没有字符串格式化/数字转串能力, 拼证据只能靠方法;
// 输入 Fact 推理期间只读, 但调用这类只读方法是安全的(同 when 里调方法)。
func (t *Transaction) Evidence() string {
	return fmt.Sprintf("amount=%.0f new_device=%t inflow24h=%.0f outflow=%.2f night=%.2f txn24h=%d", t.Amount, t.IsNewDevice, t.Inflow24h, t.OutflowRatio, t.NightRatio, t.TxnCount24h)
}

// HitRecord 一条结构化命中规则的证据 —— HitRules []string 雏形的生产升级形态:
// RuleID/Score 供报表聚合与分值对账, Evidence/HitAt 供审计与申诉回溯。
// GRL 造不了 Go 结构体、只能传标量参数 —— 组装收在 HitRec 方法的 Go 侧。
type HitRecord struct {
	RuleID   int64     `json:"rule_id"`   // 稳定主键, 千位段编码(1xxx=AML洗钱 2xxx=PAY支付 3xxx=GRADE标定 4xxx=CTRL决策): 报表/申诉按它聚合定位
	RuleName string    `json:"rule_name"` // 展示名, 如 "大额快进快出"
	Score    float64   `json:"score"`     // 本条贡献分值(须与累分赋值语句一致, 见 HitScoreSum)
	Evidence string    `json:"evidence"`  // 触发时刻的特征快照(Txn.Evidence 生成)
	HitAt    time.Time `json:"hit_at"`    // 命中时间, HitRec 内部打点(GRL 没有时间能力)
	// ResultSnap 命中落账时的 Result 快照(输出侧现场, 与 Evidence 的输入侧相对)。
	// 快照里的 score 是"本条已加上"之后的累计分 —— 依赖 GRL then 里
	// HitRec 排在累分赋值之后(四条打分规则皆如此); 打分层命中时
	// level/decision 通常仍是空串, 恰好可视化"先打分、后标定、再决策"的分层时序
	ResultSnap string `json:"result_snap"`
}

// RiskResult 决策结果 —— 可写输出事实(规则回写的唯一对象)
//
// 写入方式约定(grule 工作内存缓存机制决定, 是本示例最重要的约定):
//
//	· Score / Decision 出现在多条规则的 when 中
//	  → GRL 内必须用【赋值语句】修改(自动使缓存失效),
//	    或方法调用后显式补 Changed("Result.Score")
//	· Hits 不出现在任何 when 中
//	  → 可安全用方法 HitRec() 追加, 无需通知工作内存
type RiskResult struct {
	// Score 累计风险分, 由打分层规则逐条累加, 初值 0
	// 参与 when: ThresholdBlock / FinalManual / FinalPass
	// 修改方式: 仅限 GRL 赋值语句 Result.Score = Result.Score + x
	Score float64 `json:"score"`
	// Threshold 直接决策阈值(短路线)
	// 刻意做成"每次注入的数据"而非硬编码进 GRL:
	// 调阈值只改 Go 侧注入值, 规则文本零改动(对应优化闭环的"调整阈值")
	Threshold float64 `json:"threshold"`
	// Decision 最终决策, 取值: "拦截/拒绝" | "人工审核" | "通过" | 空串 "" 表示尚未决策 —— 兜底层规则以此作守卫,
	// 配合分数轴的互斥划分, 保证 Decision 恰好被赋值一次
	// 修改方式: 仅限 GRL 赋值语句 Result.Decision = "..."
	Decision string `json:"decision"`
	// RiskLevel 风险等级标签, 空串表示未标定。
	// 被 GRADE-001 的 when 引用作守卫; 写入走【方法 SetRiskLevel + Changed】
	// —— Changed 自我证伪示范专用字段
	RiskLevel string `json:"risk_level"`
	// Hits 结构化命中证据链(由 []string 雏形升级而来, 见 HitRecord)
	// 只写不读纪律: 不出现在任何 when 中, 方法追加才是安全的;
	// 未来若要按命中情况做闸门(如"已命中 AML 域则加严"), 另立赋值语句
	// 维护的标量字段, 勿让 when 读本切片(否则每次追加都得 Changed)
	Hits []HitRecord `json:"hits"`
}

// AddScore 累加风险分。
// ⚠ 警告: 在 GRL 中调用本方法不会通知工作内存(Score 的缓存不失效),
// 会导致依赖 Score 的规则读到旧值。规则内改分请用赋值语句;
// 本方法仅保留给 Go 侧使用(见反面教材演示)。
func (r *RiskResult) AddScore(s float64) { r.Score += s }

// HitRec 追加一条结构化命中规则的证据。
// Hits 不被任何 when 引用 → GRL 中可安全用方法追加, 无需 Changed;
// 结构体组装/时间打点收在 Go 侧: GRL 只能传标量与 Fact 表达式。
// ⚠ 参数类型与 GRL 严格对应(反射不做数值转换): 分值字面量必须写小数(80.0);
// ruleID 是同一条规则的另一面: GRL 整型字面量(1001)按 int64 传参,
// 签名必须声明 int64 —— 与 Txn.TxnCount24h 这类 int64 字段作参数同理。
func (r *RiskResult) HitRec(ruleID int64, ruleName string, score float64, evidence string) {
	r.Hits = append(r.Hits, HitRecord{
		RuleID: ruleID, RuleName: ruleName, Score: score, Evidence: evidence, HitAt: time.Now(),
		ResultSnap: fmt.Sprintf("score=%.0f level=%q decision=%q threshold=%.0f", r.Score, r.RiskLevel, r.Decision, r.Threshold),
	})
}

// HitIDs 提取命中规则 ID 清单 —— tracer 执行后快照的紧凑形式。
func (r *RiskResult) HitIDs() []int64 {
	ids := make([]int64, len(r.Hits))
	for i, h := range r.Hits {
		ids[i] = h.RuleID
	}
	return ids
}

// HitScoreSum 命中记录的分值合计, 用于与 Score 对账。
// 分值在 GRL 里结构性地写两遍(累分走赋值语句、记录走 HitRec 参数),
// 这是 grule 缓存机制决定的(加分不能收进方法, 见 AddScore 警告) ——
// 重复无法消除, 只能用对账校验兜住两处漂移。
func (r *RiskResult) HitScoreSum() float64 {
	sum := 0.0
	for _, h := range r.Hits {
		sum += h.Score
	}
	return sum
}

// SetRiskLevel 写入风险等级。GRADE-001 在 GRL 里调用它并紧跟
// Changed("Result.RiskLevel") —— 方法写被 when 引用的字段必须手动失效缓存。
func (r *RiskResult) SetRiskLevel(lv string) { r.RiskLevel = lv }

// Decide 写入最终决策。
// ⚠ 同 AddScore 的警告: Decision 被 when 引用, GRL 内请用赋值语句;
// 本方法保留给 Go 侧兜底(如引擎执行失败后的降级决策)。
func (r *RiskResult) Decide(d string) { r.Decision = d }

// Order 待处理订单 —— 三动词组合示范(demoAllVerbs)的专用 Fact
type Order struct {
	Status string    // "PENDING" → "PROCESSED"; 被 when 引用, 用赋值修改(自动失效缓存)
	Items  []float64 // 行金额
	Total  float64   // 由 CalculateTotal() 内部汇总 —— 方法修改, 必须配 Changed
}

// CalculateTotal 汇总行金额到 Total。方法内部修改了被下游 when 引用的字段,
// 工作内存不知情 —— GRL 里调用后必须紧跟 Changed("Order.Total")。
func (o *Order) CalculateTotal() {
	sum := 0.0
	for _, v := range o.Items {
		sum += v
	}
	o.Total = sum
}

// ============================================================
// 规则层 (GRL)
// 生产环境放独立 .grl 文件, 用 pkg.NewFileResource 加载;
// 这里为单文件演示用字符串 + NewBytesResource
//
// ── 三个流程控制动词对【评估周期】的影响(结合日志观察) ──
//
// Retract("规则名") —— 从本次推理会话移出该规则
//   · 下个 cycle 起该规则不再被求值(Debug 级的"求值未命中"日志都不会再有);
//     作用域仅本次 Execute, 新的 KnowledgeBase 实例自动复原
//   · 周期影响: 冲突集"出清"的主要手段 —— 打分规则每执行一条就撤一条,
//     冲突集逐轮缩小, 直到为空 → 推理自然收敛
//   · 反面: when 恒真又不 Retract → 每轮都胜出 → MaxCycle 兜底(反面教材)
//
// Complete() —— 立即终止整个推理
//   · 本条 then 执行完后, 不再进入下一 cycle, Execute 直接返回
//   · 周期影响: 全局短路。场景1: cycle 3 ThresholdBlock 执行后戛然而止,
//     没有 cycle 4, 兜底层规则连求值机会都没有 —— 这才是"直接决策"
//
// Changed("Fact.Field") —— 手动失效工作内存中该字段的缓存
//   · 背景: 赋值语句(Result.Score = ...)会【自动】失效相关缓存;
//     方法调用(Result.SetRiskLevel(...))不会 —— 引用该字段的 when 下一轮
//     仍读旧缓存; 反面教材实测更狠: 同一 then 重复执行时方法调用本身都被缓存跳过
//   · 周期影响: 决定"本轮的事实修改能否被后续周期看见", 从而支撑第三种
//     防重触发手法 —— 【自我证伪】: GRADE-001 的 when 引用 RiskLevel,
//     then 用方法写入等级后 Changed("Result.RiskLevel") → 下一轮它自己的
//     when 读到新值变假, 自然退出冲突集, 全程不需要 Retract(场景2:
//     cycle 2 执行, cycle 3 就不在冲突集里了);
//     若漏掉 Changed: when 永远读到旧的空串 → 每轮重新胜出 → 死循环
//     直到 MaxCycle 报错(与反面教材同病, 且它 salience=10 会一直压着兜底层)
//
// 防重触发三种手法对照(本文件全齐):
//   打分层 → Retract(条件靠只读 Txn, 无法自证伪, 只能自我撤出)
//   兜底层 → 守卫字段 + 赋值(Decision=""守卫, 赋值自动失效缓存, 天然自证伪)
//   标定层 → 方法 + Changed(GRADE-001, 手动失效缓存后自证伪)
//
// 组合使用(见 demoAllVerbs 的 ProcessOrder): 三者可同场协作、各司其职 ——
//   Changed 管数据流(给后续周期备好新值), Retract 管防重, Complete 管流程
//   (终止推理)。注意主次: Complete 一出, 本轮之后再无周期, Retract 与
//   Changed 的即时效果归零、退化为"防御层"。A/B 对比: A(含 Complete)
//   AuditTotal 永无机会; B(去掉 Complete) cycle 2 里 AuditTotal 靠 Changed
//   读到新 Total 而触发。健壮写法正应如此: 即使日后有人删掉 Complete,
//   规则也不会死循环(Retract 兜着)、下游也不会读旧值(Changed 兜着)。
// ============================================================

const grl = `
// ╔════════════════════════════════════════════════════════════╗
// ║ salience 频段规划(三层互不重叠, 数字大者先执行)              	 ║
// ║                                                            ║
// ║   1000    抢占层: 阈值短路, 条件一旦成立即碾压一切               ║
// ║   100~75  打分层: 业务规则, 洗钱域(100/90) > 支付域(80/75)     ║
// ║   10      标定层: 风险等级标定(Changed 自我证伪示范)            ║
// ║   2~1     兜底层: 聚合决策, 打分层全部出清后才轮到               ║
// ║                                                            ║
// ║ 兜底层三条决策对分数轴的划分【完整且互斥】:                       ║
// ║   [0,40) → 通过   [40,阈值) → 人工   [阈值,∞) → 拦截          ║
// ║ 配合 Decision=="" 守卫, 保证决策恰好落一次, 不漏判不互踩         ║
// ╚════════════════════════════════════════════════════════════╝
// - AML = Anti-Money Laundering（反洗钱）。这是金融风控行业的标准术语，不是这个示例自造的。AML-001 大额快进快出、AML-002 夜间高频交易 都是典型的洗钱行为特征检测规则，所以归入"洗钱(AML)"风险域。
// - GRADE = Grade（等级、评级），这里表示风险定级/标定。GRADE-001 做的事就是把累计分数翻译成风险等级——调用 SetRiskLevel 标定 RiskLevel=HIGH（examples/3main.go:420），所以它所在的层叫"标定层"。
// - CTRL = Control（控制、管控），表示决策控制类规则。CTRL-001 短路拦截、CTRL-002 转人工、CTRL-003 放行——都是拿汇总后的分数做最终处置决定的规则，对应"抢占层/兜底层（聚合决策）"。
// ────────────────────────────────────────────────────────────
// 规则编号: AML-001                    风险域: 洗钱(AML)
// 业务含义: 大额资金快进快出 —— 24h 大额流入且九成以上随即流出,  典型的过账/资金通道行为
// 触发条件: 24h流入 > 50万元  且  流出占比 > 0.9
// 风险分值: +80(高危)
// 优先级  : salience 100 —— 打分层最高, 洗钱域最先评估
// 执行动作: 打日志(LogFormat) → 累分(赋值语句,自动失效缓存)
//           → 记结构化证据(HitRec 方法, 分值须与累分一致) → Retract 自我撤出
// Retract 原因: when 只依赖只读的 Txn, 执行后条件仍为真,
//               不撤出会在后续 cycle 无限重触发
// ────────────────────────────────────────────────────────────
rule AMLFastInOut "洗钱-大额快进快出" salience 100 {
    when
        Txn.Inflow24h > 500000.0 && Txn.OutflowRatio > 0.9
    then
        LogFormat("AML-001 触发: 大额快进快出 24h流入=%v → +80分", Txn.Inflow24h);
        Result.Score = Result.Score + 80.0;
        Result.HitRec(1001, "大额快进快出", 80.0, Txn.Evidence());
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
        LogFormat("AML-002 触发: 夜间高频 夜间占比=%v → +45分", Txn.NightRatio);
        Result.Score = Result.Score + 45.0;
        Result.HitRec(1002, "夜间高频交易", 45.0, Txn.Evidence());
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
        LogFormat("PAY-001 触发: 新设备大额支付 金额=%v → +40分", Txn.Amount);
        Result.Score = Result.Score + 40.0;
        Result.HitRec(2001, "新设备大额支付", 40.0, Txn.Evidence());
        Retract("PayNewDeviceLarge");
}

// ────────────────────────────────────────────────────────────
// 规则编号: PAY-002                    风险域: 支付
// 业务含义: 超大额交易 —— 单笔金额显著超出常规区间, 直接高危分值
// 触发条件: 单笔金额 > 50万元
// 风险分值: +80(高危)
// 优先级  : salience 75 —— 支付域次序第二
// 执行动作: 同打分层三段式(打日志 → 赋值累分 → 记证据 → Retract)
// ────────────────────────────────────────────────────────────
rule PayHugeAmount "支付-超大额交易" salience 75 {
    when
        Txn.Amount > 500000.0
    then
        LogFormat("PAY-002 触发: 超大额交易 金额=%v → +80分", Txn.Amount);
        Result.Score = Result.Score + 80.0;
        Result.HitRec(2002, "超大额交易", 80.0, Txn.Evidence());
        Retract("PayHugeAmount");
}

// ────────────────────────────────────────────────────────────
// 规则编号: GRADE-001                  层级: 标定层(Changed 专属示范)
// 业务含义: 分数达到关注线(≥40)后给交易打风险等级标签, 供下游
//           (人工审核台/报表)使用 —— 与决策解耦的旁路标定
// 触发条件: 尚未标定(RiskLevel=="") 且 Score ≥ 40
// 优先级  : salience 10 —— 打分层出清后、兜底决策前
// 防重触发: 【方法 + Changed 的自我证伪】(不用 Retract, 不用 Complete):
//   when 引用 Result.RiskLevel; then 用方法 SetRiskLevel 写入等级 ——
//   方法调用不会自动失效工作内存缓存, 必须紧跟 Changed("Result.RiskLevel");
//   缓存失效后, 下一轮本规则的 when 重读到 "HIGH" ≠ "" → 条件变假,
//   自然退出冲突集 —— 这就是 Changed 支撑的第三种防重手法。
// ★ 对比实验: 把 Changed 那行注释掉重跑场景2 —— when 永远读到旧的
//   空串, 本规则(salience 10)每轮都压着 FinalManual(2) 重复胜出,
//   直到 MaxCycle=100 报错 —— 漏 Changed 在这里直接表现为死循环。
// ────────────────────────────────────────────────────────────
rule GradeHighRisk "标定-风险等级(方法+Changed 自我证伪)" salience 10 {
    when
        Result.RiskLevel == "" && Result.Score >= 40.0
    then
        LogFormat("GRADE-001 触发: 分数=%v → 方法标定 RiskLevel=HIGH + Changed", Result.Score);
        Result.SetRiskLevel("HIGH");
        Changed("Result.RiskLevel");
        Result.HitRec(3001, "风险等级标定HIGH", 0.0, Txn.Evidence());
}

// ────────────────────────────────────────────────────────────
// 规则编号: CTRL-001                   层级: 抢占层(短路)
// 业务含义: 累计分数触达阈值 → 不再等剩余规则, 立即拦截
//           (对应架构图"当前风险分数 ≥ 阈值 → 直接决策")
// 触发条件: Score >= Threshold(阈值随请求注入, 非硬编码)
// 优先级  : salience 1000 —— 必须是全场最高:
//           条件一旦成立, 下个 cycle 它就是冲突集第一名,
//           低优先级规则连执行机会都没有 → 这才是真短路
// 执行动作: 写决策(赋值语句) → 记证据(HitRec 放在 Complete 之前,
//           且在写决策之后 —— 快照才能带上终局决策) → Complete() 终止推理
// 无需 Retract: Complete 已终止会话
// 决策/标定类规则贡献分值为 0.0: 只留痕不改分, 分值对账不受影响
// ────────────────────────────────────────────────────────────
rule ThresholdBlock "分数达到阈值立即拦截(短路)" salience 1000 {
    when
        Result.Score >= Result.Threshold
    then
        LogFormat("CTRL-001 触发: 分数=%v 已达阈值 → 短路拦截, Complete() 终止推理", Result.Score);
        Result.Decision = "拦截/拒绝";
        Result.HitRec(4001, "阈值短路拦截", 0.0, Txn.Evidence());
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
        LogFormat("CTRL-002 触发: 分数=%v 落入灰区 [40,阈值) → 转人工", Result.Score);
        Result.Decision = "人工审核";
        Result.HitRec(4002, "灰区转人工", 0.0, Txn.Evidence());
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
        LogFormat("CTRL-003 触发: 低分=%v → 放行", Result.Score);
        Result.Decision = "通过";
        Result.HitRec(4003, "低分放行", 0.0, Txn.Evidence());
}
`

// ────────────────────────────────────────────────────────────
// 反面教材(勿模仿): 同时踩中两个坑的恒真规则
//
//	坑1: when 只依赖 Txn, then 不改变 when 引用的任何数据
//	     → 条件永真, 且无 Retract → 每个 cycle 都被重新选中
//	坑2: 用方法 AddScore 改分 → 工作内存不知情(即使 when 引用了
//	     Score 也读不到新值)
//	     ▷ 快照实测(v1.20.4): score 停在 1,1,1,1 而非 1,2,3,4 ——
//	       同一条 then 重复执行时, AddScore(1.0) 这个表达式的求值结果
//	       被工作内存缓存, 第 2 轮起【方法根本不再被真正调用】;
//	       赋值语句之所以安全, 就是因为它会主动失效相关缓存
//	结局: 触发 MaxCycle 保护, 引擎返回错误
//	(接上追踪监听器后, 能在日志里看到它每个 cycle 都胜出执行)
//
// ────────────────────────────────────────────────────────────
const grlInfiniteLoop = `
rule NoRetract "忘记Retract的规则" {
    when
        Txn.Amount > 0.0
    then
        Log("NoRetract 又执行了一次(没有 Retract 的代价: 每个 cycle 都会重来)");
        Result.AddScore(1.0);
}
`

// grlAllVerbs 三动词组合: 一条规则内 Changed / Retract / Complete 各司其职,
// 再配一条下游审计规则观察 Changed 的效果与 Complete 的支配作用
const grlAllVerbs = `
rule ProcessOrder "处理订单" salience 50 {
    when
        Order.Status == "PENDING"
    then
        Order.CalculateTotal();     // 内部修改(方法, 工作内存不知情)
        Changed("Order.Total");     // 手动失效缓存: 下一轮引用 Total 的 when 才读得到新值
        Order.Status = "PROCESSED"; // 赋值自动失效缓存 → 本规则 when 自我证伪(防重第一层)
        Retract("ProcessOrder");    // 防止重复(防重第二层)
        Complete();                 // 关键步骤完成 → 立即终止本轮推理(支配一切)
}

rule AuditTotal "审计-读取订单总额(Changed 的下游观察者)" salience 40 {
    when
        Order.Total > 0.0
    then
        LogFormat("AUDIT 触发: 读到订单总额=%v —— 依赖 ProcessOrder 里的 Changed", Order.Total);
        Retract("AuditTotal");
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

/*
KnowledgeLibrary与KnowledgeBase实例
Grule-Rule-Engine通过KnowledgeLibrary来管理规则库，从KnowledgeLibrary获得的每一个KnowledgeBase实例都是唯一的克隆。每个唯一的实例都有自己独立的WorkingMemory，不会共享状态给其他实例，这使得我们可以在多线程中安全地使用这些实例。
*/
// evaluate 对一笔交易执行一次完整推理, 并挂载 cycle 追踪监听器。
// 关键: KnowledgeBaseInstance 内含工作内存(求值缓存+Retract状态),
// 是有状态对象 —— 并发场景每个 goroutine/每笔请求各取一个, 严禁共享。
// GruleEngine 本身也按请求新建, Listeners 挂在引擎上。
func evaluate(zlog *zap.Logger, lib *ast.KnowledgeLibrary, label string, txn *Transaction) {
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

	fmt.Printf("\n━━━━━━ %s ━━━━━━\n", label)

	// eng1 := &engine.GruleEngine{MaxCycle: 100,Listener: &MyListener{} } 设置评估周期数-使用监听器
	eng := engine.NewGruleEngine()
	eng.MaxCycle = 100                                        // 推理预算(默认 5000): 防恒真规则的最后保险
	tracer := &CycleTraceListener{zlog: zlog, result: result} // 挂载执行追踪: 冲突集 / 执行前后快照(Before/After) 逐 cycle 打印
	eng.Listeners = []engine.GruleEngineListener{tracer}

	if err := eng.Execute(dctx, kb); err != nil {
		log.Fatalf("引擎执行失败: %v", err)
	}
	tracer.Finish() // 补发最后一轮的 AfterExecuteRule

	// 业务结论走 fmt(与 zap 的执行轨迹流区分开)
	level := result.RiskLevel
	if level == "" {
		level = "-"
	}
	fmt.Printf("   总分: %.0f | 等级: %s | 决策: %s\n", result.Score, level, result.Decision)
	for _, h := range result.Hits {
		fmt.Printf("   命中: %-6d %s %+3.0f分 @%s  [%s] ⇒ [%s]\n", h.RuleID, h.RuleName, h.Score, h.HitAt.Format(time.DateTime), h.Evidence, h.ResultSnap)
	}
	// 分值(Score)对账: 加分与记录必然双写(见 HitScoreSum), 漂移在这里现形
	if math.Abs(result.Score-result.HitScoreSum()) > 1e-9 {
		fmt.Printf("   ⚠ 分值对账不平: Score=%.0f ≠ Σhits=%.0f\n", result.Score, result.HitScoreSum())
	}
}

// demoInfiniteLoop 演示反面教材的实际后果:
// 追踪日志会显示同一条规则每个 cycle 都胜出执行,
// 直到 MaxCycle 保护触发, Execute 返回错误。
func demoInfiniteLoop(zlog *zap.Logger) {
	lib := ast.NewKnowledgeLibrary()
	rb := builder.NewRuleBuilder(lib)
	if err := rb.BuildRuleFromResource("loop", "1.0.0", pkg.NewBytesResource([]byte(grlInfiniteLoop))); err != nil {
		log.Fatal(err)
	}
	kb, _ := lib.NewKnowledgeBaseInstance("loop", "1.0.0")

	result := &RiskResult{}
	dctx := ast.NewDataContext()
	dctx.Add("Txn", &Transaction{Amount: 100})
	dctx.Add("Result", result)

	fmt.Printf("\n━━━━━━ 反面教材: 无 Retract 的恒真规则 ━━━━━━\n")

	eng := engine.NewGruleEngine()
	eng.MaxCycle = 4 // 调小以快速触发保护(追踪日志逐 cycle 可见)
	tracer := &CycleTraceListener{zlog: zlog, result: result}
	eng.Listeners = []engine.GruleEngineListener{tracer}

	err := eng.Execute(dctx, kb)
	tracer.Finish() // MaxCycle 中止后同样补发最后一次执行的 After
	fmt.Printf("   引擎返回错误: %v\n", err)
}

// demoAllVerbs 三动词同场协作(ProcessOrder) + A/B 对比:
// A 原样执行(含 Complete): cycle 1 处理完订单即终止, AuditTotal 永无机会;
// B 注释掉 Complete 重跑: cycle 2 里 AuditTotal 靠 Changed 读到新 Total 并触发,
//
//	cycle 3 冲突集空、自然收敛 —— Retract 保证 ProcessOrder 不会二次执行。
//
// (再进一步的实验: 把 Changed 也注释掉 → B 变体里 AUDIT 读到的 Total 恒为
//
//	旧缓存 0, 永不触发, 推理静默结束 —— 三个动词各自的缺席都有独特病症)
func demoAllVerbs(zlog *zap.Logger) {
	runOnce := func(label string, farr []float64, grlText string) {
		// fmt.Println("规则=", grlText)
		lib := buildKnowledgeLibrary("allverbs", "1.0.0", grlText)
		kb, err := lib.NewKnowledgeBaseInstance("allverbs", "1.0.0")
		if err != nil {
			log.Fatalf("获取知识库实例失败: %v", err)
		}

		order := &Order{Status: "PENDING", Items: farr}
		dctx := ast.NewDataContext()
		if err := dctx.Add("Order", order); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("\n━━━━━━ %s ━━━━━━\n", label)

		eng := engine.NewGruleEngine()
		eng.MaxCycle = 10
		tracer := &CycleTraceListener{zlog: zlog, result: &RiskResult{}} // 快照占位, 本演示看 Order
		eng.Listeners = []engine.GruleEngineListener{tracer}

		if err := eng.Execute(dctx, kb); err != nil {
			log.Fatalf("引擎执行失败: %v", err)
		}
		tracer.Finish()

		fmt.Printf("   订单: status=%s total=%.0f\n", order.Status, order.Total)
	}

	runOnce("组合A: Changed+Retract+Complete 同一条规则", []float64{100, 200, 300}, grlAllVerbs)
	runOnce("组合B: 同一规则注释掉 Complete()", []float64{200, 300, 400},
		strings.Replace(grlAllVerbs, "Complete();", "// Complete(); (B 变体注释掉)", 1))
}

// ============================================================
// ★ 第二部分: 决策表 / 决策树 / 决策流 ★
//
// ═══ 对第一部分(3main.go)的深入分析: 三种决策构件早已在里面埋着 ═══
//
// 上面那套四层 salience 频段(抢占1000/打分100~75/标定10/兜底2~1)拆开看:
//
//	· 打分层 4 条规则  = "散装规则集"(可叠加命中, Retract 防重) —— 规则的最原始形态;
//	· 兜底层 3 条规则  = 一张【一维决策表】: 对 Score 数轴做完整互斥分段
//	  ([0,40)→通过 [40,阈值)→人工 [阈值,∞)→拦截), 守卫 Decision=="" 就是表的
//	  first-hit 语义 —— 只是没人把它"叫"决策表;
//	· 抢占层 Complete() = 【决策流短路】的节点内形态 —— 只是整个流程被压在
//	  一个知识库里, 短路半径 = 全部规则。
//
// 第二部分把这三个隐式构件显式化、结构化:
//
//	构件     本质                  grule 表达心法
//	──────────────────────────────────────────────────────────────────
//	决策表   规则的【矩阵化】组织    一个单元格=一条规则; when=行条件∧列条件;
//	        (条件维度固定/组合有限)  互斥靠行列取值+守卫字段(first-hit);
//	                              必须配【兜底行】堵覆盖缺口
//	决策树   规则的【层级化】组织    拍平法则: 一片叶子=一条规则,
//	        (条件不对称/有先后)     when=根到叶的全路径条件;
//	                              互斥性来自路径划分, 守卫只是保险
//	决策流   规则集的【阶段化】组织   一个节点=一个独立知识库;
//	        (阶段依赖/短路/热更新)   数据靠"总线 fact"跨节点传递;
//	                              Complete 管节点内短路, Go 编排管流级短路
//	决策图   规则集的【依赖化】组织   节点=知识库, 边=数据依赖(DAG);
//	        (分支+汇聚/并行评估)     拓扑分层调度, 同层节点并发执行;
//	                              决策流是它的退化形态(单后继的链)
//
// 四者共用的 grule 纪律(第一部分反复验证过的原样适用):
// 被 when 引用的字段只能赋值语句写; 守卫字段赋值后自证伪天然防重;
// 只读输入的打分规则必须 Retract; Complete 只终止本知识库。
// 怎么选: 条件维度整齐 → 表; 条件不对称层层收窄 → 树;
// 需要阶段隔离/独立热更新/控制短路半径 → 流;
// 多域并行评估再汇聚(fan-out/fan-in) → 图。
// ============================================================

// Portrait 客户画像 —— 第二部分新增的只读输入 fact
// (交易特征继续复用第一部分的 Transaction; 画像给决策树/决策表用)
type Portrait struct {
	Age         float64
	Income      float64
	DeviceScore float64 // 设备可信分 0~100 | 被引用: 树叶3/叶4
	IsNew       bool    // 新客/老客 | 被引用: 树的第二层分叉 + 决策表列条件
}

// Pipe 决策流"数据总线" fact —— 跨节点传递的唯一载体。
//
// 关键设计: 单个知识库内, 规则通过【工作内存】耦合; 跨知识库(跨节点)没有
// 共享工作内存 —— 数据必须由 Go 编排层带着走, 手法就是把同一个 *Pipe 指针
// 依次注入每个节点的 DataContext。上游节点写的字段, 下游节点的 when 直接
// 引用(决策树的叶1 就引用了打分节点写的 Pipe.Score)。
//
// 写入纪律(同第一部分): Score/Blocked/Grade/Limit/Action 都被某个 when
// 引用 → 只能赋值语句写; Hits 只写不读 → 方法追加安全。
type Pipe struct {
	Threshold float64 // 短路阈值(Go 侧注入, 不硬编码进 GRL, 同 RiskResult.Threshold)

	// 节点① 打分规则集 → 写
	Score   float64
	Blocked bool // 流级短路信号: 节点①置位, Go 编排层读它决定是否继续走流

	// 节点② 决策树 → 写
	Grade string  // A / B / C(客群等级)
	Limit float64 // 授信额度

	// 节点③ 决策表 → 写
	Action  string  // 处置动作
	RateBps float64 // 费率(基点)

	// 全程证据链(只写不读, 方法追加, 同 RiskResult.Hits 的纪律)
	Hits []string
}

// Hit 追加证据。Hits 不被任何 when 引用, GRL 中可安全调用。
func (p *Pipe) Hit(s string) { p.Hits = append(p.Hits, s) }

// ─────────────── ① 决策表 (Decision Table) ───────────────
//
// 定义: 条件维度固定、取值有限时, 把规则组织成"行×列 → 单元格"的矩阵。
// 本例二维交叉表(行=客群等级 Grade, 列=新老客 IsNew), 单元格=处置动作+费率:
//
//	               IsNew=false(老客)       IsNew=true(新客)
//	Grade=A        提额邀约   30bps        高额起批   50bps
//	Grade=B        标准授信   60bps        标准授信+首刷礼 80bps
//	Grade=C        降额观察  100bps        小额试用  120bps
//	(兜底行)        ——— 人工复核: 表没覆盖到的组合唯一会落进来的行 ———
//
// grule 表达要素(解剖一格即懂全表):
//
//	· 一个单元格 = 一条规则; when = 守卫 ∧ 行条件 ∧ 列条件 —— 全表条件结构
//	  完全同构, 这正是"表"区别于第一部分打分层"散装规则"的地方:
//	  改业务 = 加行/改格, 不动结构;
//	· first-hit 语义 = 守卫 Pipe.Action=="" + 单元格互斥(行列取值天然互斥);
//	  DMN 的 hit policy F(First)/U(Unique) 在 grule 里都是这一套写法;
//	· 【兜底行】是决策表的工程底线: 行列组合出现覆盖缺口(比如 Grade 出现
//	  意料外的值)时, 没有兜底行 = 推理静默收敛、输出为空(第一部分讲过
//	  "规则悄悄失效比崩溃更隐蔽"), 有兜底行 = 显式进人工 —— 演示"表②"
//	  专门构造了这个缺口;
//	· salience 只定"检查顺序"(可读性), 正确性不依赖它 —— 互斥表怎么排都只中一格。
const grlDecisionTable = `
rule CellAOld "表格: A×老客 → 提额邀约" salience 60 {
    when
        Pipe.Action == "" && Pipe.Grade == "A" && Pt.IsNew == false
    then
        LogFormat("[决策表] A×老客 → 提额邀约 30bps (score=%v)", Pipe.Score);
        Pipe.Action = "提额邀约";
        Pipe.RateBps = 30.0;
        Pipe.Hit("TABLE A×老客→提额邀约");
}

rule CellANew "表格: A×新客 → 高额起批" salience 50 {
    when
        Pipe.Action == "" && Pipe.Grade == "A" && Pt.IsNew == true
    then
        LogFormat("[决策表] A×新客 → 高额起批 50bps (score=%v)", Pipe.Score);
        Pipe.Action = "高额起批";
        Pipe.RateBps = 50.0;
        Pipe.Hit("TABLE A×新客→高额起批");
}

rule CellBOld "表格: B×老客 → 标准授信" salience 40 {
    when
        Pipe.Action == "" && Pipe.Grade == "B" && Pt.IsNew == false
    then
        LogFormat("[决策表] B×老客 → 标准授信 60bps (score=%v)", Pipe.Score);
        Pipe.Action = "标准授信";
        Pipe.RateBps = 60.0;
        Pipe.Hit("TABLE B×老客→标准授信");
}

rule CellBNew "表格: B×新客 → 标准授信+首刷礼" salience 30 {
    when
        Pipe.Action == "" && Pipe.Grade == "B" && Pt.IsNew == true
    then
        LogFormat("[决策表] B×新客 → 标准授信+首刷礼 80bps (score=%v)", Pipe.Score);
        Pipe.Action = "标准授信+首刷礼";
        Pipe.RateBps = 80.0;
        Pipe.Hit("TABLE B×新客→首刷礼");
}

rule CellCOld "表格: C×老客 → 降额观察" salience 20 {
    when
        Pipe.Action == "" && Pipe.Grade == "C" && Pt.IsNew == false
    then
        LogFormat("[决策表] C×老客 → 降额观察 100bps (score=%v)", Pipe.Score);
        Pipe.Action = "降额观察";
        Pipe.RateBps = 100.0;
        Pipe.Hit("TABLE C×老客→降额观察");
}

rule CellCNew "表格: C×新客 → 小额试用" salience 10 {
    when
        Pipe.Action == "" && Pipe.Grade == "C" && Pt.IsNew == true
    then
        LogFormat("[决策表] C×新客 → 小额试用 120bps (score=%v)", Pipe.Score);
        Pipe.Action = "小额试用";
        Pipe.RateBps = 120.0;
        Pipe.Hit("TABLE C×新客→小额试用");
}

// 兜底行: salience 最低, 只有全表没命中(守卫仍为空)才轮到。
// 这一行没有行列条件, 恒可命中, 防重完全依赖守卫自证伪
// (赋值后 Action != "" → 下轮自动退出冲突集, 同第一部分兜底层手法)。
rule CellFallback "表格: 兜底行 → 人工复核" salience 1 {
    when
        Pipe.Action == ""
    then
        LogFormat("[决策表] ⚠ 兜底行命中: 未覆盖的组合 Grade=%v → 人工复核", Pipe.Grade);
        Pipe.Action = "人工复核";
        Pipe.RateBps = 0.0;
        Pipe.Hit("TABLE 兜底行→人工复核");
}
`

// ─────────────── ② 决策树 (Decision Tree) ───────────────
//
// 定义: 条件不对称、有天然判断先后时, 把规则组织成层级分叉。本例三层:
//
//	Score>=40(上游打分中高风险) ──────────────────────── 叶1: C 额度5000
//	Score<40 ┬ 新客 ┬ Age<22 ─────────────────────────── 叶2: C 额度8000
//	         │      └ Age>=22 ┬ DeviceScore>=80 ──────── 叶3: B 额度20000
//	         │                └ DeviceScore<80 ───────── 叶4: C 额度10000
//	         └ 老客 ┬ Income>=20000 ──────────────────── 叶5: A 额度50000
//	                └ Income<20000 ───────────────────── 叶6: B 额度30000
//
// grule 表达: 【拍平法则】—— GRL 没有嵌套 if, 树必须拍平成"叶子规则集":
//
//	· 一片叶子 = 一条规则, when = 根到叶的全路径条件
//	  (叶3 = Score<40 ∧ 新客 ∧ Age>=22 ∧ DeviceScore>=80);
//	· 互斥性来自"每一层分叉都是完整互斥划分"(<40/>=40, 新/老, <22/>=22...),
//	  任何输入恰好落在一片叶上 —— 守卫 Grade=="" 只是防御性保险;
//	  demoDecisionTree 挂了追踪监听器, 日志里能看到冲突集永远只有一片叶
//	  (对比第一部分: 打分层可多条同时入选 —— 这就是树和散装规则集的区别);
//	· 代价: 共享前缀条件(Score<40)在多片叶里重复出现, "改一层要同步改 N 片叶"
//	  是主要维护风险 —— 缓解手段就是本注释块的树形图: 图是唯一事实源,
//	  叶子按图生成/校对;
//	· 注意叶1引用了【上游节点的输出】Pipe.Score —— 这正是决策流里
//	  "前一节点的输出是后一节点的输入"的具体形态。
const grlDecisionTree = `
rule Leaf1 "树叶1: 中高风险 → C/5000" salience 60 {
    when
        Pipe.Grade == "" && Pipe.Score >= 40.0
    then
        LogFormat("[决策树] 叶1: 上游风险分=%v ≥40 → C 额度5000", Pipe.Score);
        Pipe.Grade = "C";
        Pipe.Limit = 5000.0;
        Pipe.Hit("TREE 中高风险→C/5000");
}

rule Leaf2 "树叶2: 低风险×新客×年轻 → C/8000" salience 50 {
    when
        Pipe.Grade == "" && Pipe.Score < 40.0 && Pt.IsNew == true && Pt.Age < 22.0
    then
        LogFormat("[决策树] 叶2: 新客年轻(age=%v) → C 额度8000", Pt.Age);
        Pipe.Grade = "C";
        Pipe.Limit = 8000.0;
        Pipe.Hit("TREE 新客年轻→C/8000");
}

rule Leaf3 "树叶3: 低风险×新客×成年×设备可信 → B/20000" salience 40 {
    when
        Pipe.Grade == "" && Pipe.Score < 40.0 && Pt.IsNew == true && Pt.Age >= 22.0 && Pt.DeviceScore >= 80.0
    then
        LogFormat("[决策树] 叶3: 新客设备可信(dev=%v) → B 额度20000", Pt.DeviceScore);
        Pipe.Grade = "B";
        Pipe.Limit = 20000.0;
        Pipe.Hit("TREE 新客设备可信→B/20000");
}

rule Leaf4 "树叶4: 低风险×新客×成年×设备存疑 → C/10000" salience 30 {
    when
        Pipe.Grade == "" && Pipe.Score < 40.0 && Pt.IsNew == true && Pt.Age >= 22.0 && Pt.DeviceScore < 80.0
    then
        LogFormat("[决策树] 叶4: 新客设备存疑(dev=%v) → C 额度10000", Pt.DeviceScore);
        Pipe.Grade = "C";
        Pipe.Limit = 10000.0;
        Pipe.Hit("TREE 新客设备存疑→C/10000");
}

rule Leaf5 "树叶5: 低风险×老客×高收入 → A/50000" salience 20 {
    when
        Pipe.Grade == "" && Pipe.Score < 40.0 && Pt.IsNew == false && Pt.Income >= 20000.0
    then
        LogFormat("[决策树] 叶5: 老客高收入(income=%v) → A 额度50000", Pt.Income);
        Pipe.Grade = "A";
        Pipe.Limit = 50000.0;
        Pipe.Hit("TREE 老客高收入→A/50000");
}

rule Leaf6 "树叶6: 低风险×老客×普通收入 → B/30000" salience 10 {
    when
        Pipe.Grade == "" && Pipe.Score < 40.0 && Pt.IsNew == false && Pt.Income < 20000.0
    then
        LogFormat("[决策树] 叶6: 老客普通收入(income=%v) → B 额度30000", Pt.Income);
        Pipe.Grade = "B";
        Pipe.Limit = 30000.0;
        Pipe.Hit("TREE 老客普通收入→B/30000");
}
`

// ─────────────── ③ 决策图 (Decision Graph) ───────────────
//
// 定义: 把决策组织成【有向无环图 DAG】—— 节点=独立知识库, 边=数据依赖。
// 决策流是决策图的退化形态(每个节点至多一个后继的"链");
// 图多出来的能力是【分支(fan-out)与汇聚(fan-in)】:
//
//	         ┌→ [AML评分] ─┐          两个评分域之间没有数据依赖
//	 input ──┤             ├→ [汇聚] → [终决]
//	         └→ [PAY评分] ─┘          → 处于同一拓扑层, 可并发执行
//
// 工业对应: GoRules zen 的 JDM 决策模型就是一张决策图(见 9main.go 的
// input→表→表达式→switch→output), DMN 的 DRD(决策需求图)同理 ——
// "图"是现代决策引擎的通用组织形态, 流/树/表都能嵌进图里当节点。
//
// grule 视角的三个要点(详细版):
//
//	· 并行的合法性: KnowledgeBase 实例各持独立工作内存(第一部分"并发模型"
//	  已述), 不同实例天然并发安全; 再给每个并行节点【独立输出 fact】
//	  (下面的 DomainScore, AML/PAY 各一个), 连共享写都消掉 ——
//	  可用 go run -race 验证。若两个并行节点写同一个 fact 的同一切片
//	  (比如共用 Pipe.Hits), 那就是数据竞争 —— 输出槽隔离是图并行的前提;
//	· 汇聚节点 = 依赖全部就绪才执行: 调度用 Kahn 拓扑分层(runGraph),
//	  每一层收集"依赖已完成"的节点并发跑, 跑完标记再收下一层;
//	  汇聚 GRL 用 G.Merged 布尔守卫防重(赋值自证伪, 同兜底层手法);
//	· 单知识库内部其实就是一张【隐式图】: 规则通过工作内存字段互相耦合
//	  (第一部分打分层写 Score、决策层读 Score, 就是一条依赖边)。
//	  决策图做的事是把这些依赖【显式化为节点边界】, 换来并行度、
//	  独立热更新与故障隔离 —— 代价是编排层代码, 收益随规则规模放大。
//
// 图级短路本例不再重复演示(决策流场景A已示范 Blocked 信号):
// 生产做法是命中阻断后通过 context 取消尚未启动的下游节点。

// DomainScore 并行评分节点的【独立输出槽】——
// AML/PAY 两个节点各持一个实例, 并行写互不相扰(-race 干净的前提)。
type DomainScore struct {
	Score float64
	Hits  []string
}

// Hit 追加证据(Hits 只写不读, GRL 方法调用安全)
func (d *DomainScore) Hit(s string) { d.Hits = append(d.Hits, s) }

// GraphResult 汇聚与终决节点的输出。
// Merged 是汇聚规则的布尔守卫; Total/Decision 被 when 引用 → 只能赋值语句写。
type GraphResult struct {
	Merged   bool
	Total    float64
	Decision string
	Hits     []string
}

// Hit 追加证据
func (g *GraphResult) Hit(s string) { g.Hits = append(g.Hits, s) }

// 图节点a: AML 域评分(只管洗钱特征, 写自己的 Out 槽)
const grlGraphAml = `
rule GAmlFastInOut "图AML: 大额快进快出 +80" salience 100 {
    when
        Txn.Inflow24h > 500000.0 && Txn.OutflowRatio > 0.9
    then
        LogFormat("[图-AML] 快进快出 流入=%v → +80", Txn.Inflow24h);
        Out.Score = Out.Score + 80.0;
        Out.Hit("AML 快进快出+80");
        Retract("GAmlFastInOut");
}

rule GAmlNightBurst "图AML: 夜间高频 +45" salience 90 {
    when
        Txn.NightRatio > 0.7 && Txn.TxnCount24h > 50
    then
        LogFormat("[图-AML] 夜间高频 占比=%v → +45", Txn.NightRatio);
        Out.Score = Out.Score + 45.0;
        Out.Hit("AML 夜间高频+45");
        Retract("GAmlNightBurst");
}
`

// 图节点b: PAY 域评分(只管支付特征, 写自己的 Out 槽) —— 与节点a无依赖, 并发执行
const grlGraphPay = `
rule GPayNewDevLarge "图PAY: 新设备大额 +40" salience 100 {
    when
        Txn.IsNewDevice == true && Txn.Amount > 50000.0
    then
        LogFormat("[图-PAY] 新设备大额 金额=%v → +40", Txn.Amount);
        Out.Score = Out.Score + 40.0;
        Out.Hit("PAY 新设备大额+40");
        Retract("GPayNewDevLarge");
}

rule GPayHugeAmount "图PAY: 超大额 +80" salience 90 {
    when
        Txn.Amount > 500000.0
    then
        LogFormat("[图-PAY] 超大额 金额=%v → +80", Txn.Amount);
        Out.Score = Out.Score + 80.0;
        Out.Hit("PAY 超大额+80");
        Retract("GPayHugeAmount");
}
`

// 图节点c: 汇聚(fan-in) —— 依赖 a、b 全部完成后才被调度;
// 读两个域的输出槽求和。Merged 布尔守卫: 赋值 true 后 when 自证伪。
const grlGraphMerge = `
rule GMerge "图汇聚: 域分求和" salience 10 {
    when
        G.Merged == false
    then
        G.Total = Aml.Score + Pay.Score;
        G.Merged = true;
        LogFormat("[图-汇聚] AML+PAY 合计=%v", G.Total);
        G.Hit("MERGE 域分汇总");
}
`

// 图节点d: 终决 —— 对 Total 的一维决策表(同第一部分兜底层/守卫手法;
// 阈值这里为演示直接写 100.0, 生产应像第一部分那样从 fact 注入)
const grlGraphFinal = `
rule GFinalBlock "图终决: ≥100 拦截" salience 30 {
    when
        G.Decision == "" && G.Total >= 100.0
    then
        LogFormat("[图-终决] total=%v ≥100 → 拦截", G.Total);
        G.Decision = "拦截/拒绝";
        G.Hit("FINAL 拦截");
}

rule GFinalManual "图终决: [40,100) 人工" salience 20 {
    when
        G.Decision == "" && G.Total >= 40.0 && G.Total < 100.0
    then
        LogFormat("[图-终决] total=%v 落灰区 → 人工审核", G.Total);
        G.Decision = "人工审核";
        G.Hit("FINAL 人工");
}

rule GFinalPass "图终决: <40 通过" salience 10 {
    when
        G.Decision == "" && G.Total < 40.0
    then
        LogFormat("[图-终决] total=%v <40 → 通过", G.Total);
        G.Decision = "通过";
        G.Hit("FINAL 通过");
}
`

// graphNode 决策图节点: 名字 + 依赖清单 + 执行体(内部跑各自的 grule 知识库)
type graphNode struct {
	name string
	deps []string
	run  func()
}

// runGraphKB 执行一个图节点的知识库: fact 组合因节点而异, 用 map 注入
func runGraphKB(lib *ast.KnowledgeLibrary, name string, facts map[string]any) {
	kb, err := lib.NewKnowledgeBaseInstance(name, "1.0.0")
	if err != nil {
		log.Fatalf("取知识库实例失败(%s): %v", name, err)
	}
	dctx := ast.NewDataContext()
	for k, v := range facts {
		if err := dctx.Add(k, v); err != nil {
			log.Fatal(err)
		}
	}
	eng := engine.NewGruleEngine()
	eng.MaxCycle = 50
	if err := eng.Execute(dctx, kb); err != nil {
		log.Fatalf("推理失败(%s): %v", name, err)
	}
}

// runGraph Kahn 拓扑分层调度器:
// 每一轮收集"依赖已全部完成"的节点为一层, 层内 goroutine 并发执行
// (WaitGroup 收齐), 层间串行推进; 收不出新层却还有剩余节点 = DSL 配置成环。
// 返回轨迹串, 并行层记作 [a ∥ b]。
func runGraph(nodes []graphNode) string {
	done := make(map[string]bool, len(nodes))
	stages := make([]string, 0, len(nodes))
	for len(done) < len(nodes) {
		var ready []graphNode
		for _, n := range nodes {
			if done[n.name] {
				continue
			}
			ok := true
			for _, d := range n.deps {
				if !done[d] {
					ok = false
					break
				}
			}
			if ok {
				ready = append(ready, n)
			}
		}
		if len(ready) == 0 {
			log.Fatal("决策图存在环, 无法调度")
		}
		var wg sync.WaitGroup
		names := make([]string, len(ready))
		for i, n := range ready {
			names[i] = n.name
			wg.Add(1)
			go func(fn func()) {
				defer wg.Done()
				fn()
			}(n.run)
		}
		wg.Wait()
		for _, n := range ready {
			done[n.name] = true
		}
		if len(names) > 1 {
			stages = append(stages, "["+strings.Join(names, " ∥ ")+"]")
		} else {
			stages = append(stages, names[0])
		}
	}
	return strings.Join(stages, " → ")
}

// demoDecisionGraph 决策图: AML/PAY 双域并行评分 → 汇聚 → 终决
func demoDecisionGraph() {
	fmt.Println("\n═══ ③ 决策图: 双域并行评分(fan-out) → 汇聚(fan-in) → 终决 ═══")
	amlLib := buildKnowledgeLibrary("g_aml", "1.0.0", grlGraphAml)
	payLib := buildKnowledgeLibrary("g_pay", "1.0.0", grlGraphPay)
	mergeLib := buildKnowledgeLibrary("g_merge", "1.0.0", grlGraphMerge)
	finalLib := buildKnowledgeLibrary("g_final", "1.0.0", grlGraphFinal)

	runOnce := func(label string, txn *Transaction) {
		fmt.Printf("\n━━━━━━ %s ━━━━━━\n", label)
		// 输出槽按节点隔离: 并行安全的前提(见节注解)
		aml, pay, g := &DomainScore{}, &DomainScore{}, &GraphResult{}
		track := runGraph([]graphNode{
			{name: "AML评分", deps: nil, run: func() {
				runGraphKB(amlLib, "g_aml", map[string]any{"Txn": txn, "Out": aml})
			}},
			{name: "PAY评分", deps: nil, run: func() {
				runGraphKB(payLib, "g_pay", map[string]any{"Txn": txn, "Out": pay})
			}},
			{name: "汇聚", deps: []string{"AML评分", "PAY评分"}, run: func() {
				runGraphKB(mergeLib, "g_merge", map[string]any{"Aml": aml, "Pay": pay, "G": g})
			}},
			{name: "终决", deps: []string{"汇聚"}, run: func() {
				runGraphKB(finalLib, "g_final", map[string]any{"G": g})
			}},
		})
		fmt.Printf("   轨迹: input → %s → output\n", track)
		fmt.Printf("   域分: AML=%.0f %v | PAY=%.0f %v\n", aml.Score, aml.Hits, pay.Score, pay.Hits)
		fmt.Printf("   终局: total=%.0f decision=%s %v\n", g.Total, g.Decision, g.Hits)
	}

	// 图场景A: 双域齐发 —— AML(80+45=125) ∥ PAY(40+80=120) → 汇聚 245 → 拦截
	runOnce("图场景A: 双域齐发(245分→拦截)", &Transaction{
		Inflow24h: 800000, OutflowRatio: 0.95, NightRatio: 0.82, TxnCount24h: 120,
		IsNewDevice: true, Amount: 600000,
	})
	// 图场景B: 仅 PAY 轻度命中 → 汇聚 40 → 人工(另一个域并行跑但零命中)
	runOnce("图场景B: 单域轻度(40分→人工)", &Transaction{
		IsNewDevice: true, Amount: 60000,
	})
}

// ─────────────── ④ 决策流的节点① —— 打分规则集 ───────────────
//
// 精简自第一部分的打分层(交易特征复用 Transaction), 外加流级短路信号。
// 决策流短路的两级分工(本节点是发起方):
//
//	· Complete()        —— 终止【本知识库】推理: 阈值规则命中后, 本节点
//	  剩余打分规则不再评估(节点内短路, 同第一部分抢占层 CTRL-001);
//	· Pipe.Blocked=true —— 给【Go 编排层】的流级信号: runFlow 每执行完
//	  一个节点就检查它, 为真则不再进入下游节点(树/表连知识库实例都不建)。
//
// grule 自身没有"多知识库编排"能力, 流级短路必须由编排层完成 ——
// 这也是决策流构件存在的理由: 短路半径从"全部规则"缩小到"一个节点"。
const grlScoreNode = `
rule ScoreThresholdBlock "打分-阈值短路(抢占)" salience 1000 {
    when
        Pipe.Score >= Pipe.Threshold
    then
        LogFormat("[打分] 风险分=%v 已达阈值 → 节点内 Complete + 流级 Blocked", Pipe.Score);
        Pipe.Blocked = true;
        Pipe.Hit("SCORE 阈值短路");
        Complete();
}

rule ScoreFastInOut "打分-大额快进快出 +80" salience 100 {
    when
        Txn.Inflow24h > 500000.0 && Txn.OutflowRatio > 0.9
    then
        LogFormat("[打分] 快进快出 24h流入=%v → +80", Txn.Inflow24h);
        Pipe.Score = Pipe.Score + 80.0;
        Pipe.Hit("SCORE 快进快出+80");
        Retract("ScoreFastInOut");
}

rule ScoreNightBurst "打分-夜间高频 +45" salience 90 {
    when
        Txn.NightRatio > 0.7 && Txn.TxnCount24h > 50
    then
        LogFormat("[打分] 夜间高频 占比=%v → +45", Txn.NightRatio);
        Pipe.Score = Pipe.Score + 45.0;
        Pipe.Hit("SCORE 夜间高频+45");
        Retract("ScoreNightBurst");
}

rule ScoreHugeAmount "打分-超大额 +80" salience 80 {
    when
        Txn.Amount > 500000.0
    then
        LogFormat("[打分] 超大额 金额=%v → +80", Txn.Amount);
        Pipe.Score = Pipe.Score + 80.0;
        Pipe.Hit("SCORE 超大额+80");
        Retract("ScoreHugeAmount");
}
`

// runPipeKB 对一个"第二部分知识库"执行一次推理:
// 注入 Txn(交易, 复用第一部分 Transaction)/Pt(画像)/Pipe(总线) 三个 fact,
// 每次全新 KnowledgeBase 实例(工作内存不共享, 第一部分的纪律)。
// tracer 可为 nil: 决策流多节点连跑挂 tracer 太吵, 独立示例才挂。
func runPipeKB(lib *ast.KnowledgeLibrary, name string, txn *Transaction, pt *Portrait, pipe *Pipe, tracer *CycleTraceListener) {
	kb, err := lib.NewKnowledgeBaseInstance(name, "1.0.0")
	if err != nil {
		log.Fatalf("取知识库实例失败(%s): %v", name, err)
	}
	dctx := ast.NewDataContext()
	if err := dctx.Add("Txn", txn); err != nil {
		log.Fatal(err)
	}
	if err := dctx.Add("Pt", pt); err != nil {
		log.Fatal(err)
	}
	if err := dctx.Add("Pipe", pipe); err != nil {
		log.Fatal(err)
	}
	eng := engine.NewGruleEngine()
	eng.MaxCycle = 50
	if tracer != nil {
		eng.Listeners = []engine.GruleEngineListener{tracer}
	}
	if err := eng.Execute(dctx, kb); err != nil {
		log.Fatalf("推理失败(%s): %v", name, err)
	}
	if tracer != nil {
		tracer.Finish()
	}
}

// demoDecisionTable 独立演示决策表: 一次命中正格, 一次故意制造覆盖缺口踩兜底行
func demoDecisionTable(zlog *zap.Logger) {
	fmt.Println("\n═══ ① 决策表: 等级×新老客 交叉定价 ═══")
	lib := buildKnowledgeLibrary("table", "1.0.0", grlDecisionTable)

	// 表①: B×老客 → 命中正格"标准授信"
	pipe := &Pipe{Grade: "B", Score: 12}
	runPipeKB(lib, "table", &Transaction{}, &Portrait{IsNew: false}, pipe, nil)
	fmt.Printf("   表①(B×老客): action=%s rate=%.0fbps hits=%v\n", pipe.Action, pipe.RateBps, pipe.Hits)

	// 表②: 故意注入表没覆盖的 Grade="X" → 六个正格全不匹配 → 兜底行接住
	// (没有兜底行的话: 推理静默收敛、Action 仍为空 —— 覆盖缺口就这样漏到线上)
	pipe2 := &Pipe{Grade: "X", Score: 12}
	runPipeKB(lib, "table", &Transaction{}, &Portrait{IsNew: true}, pipe2, nil)
	fmt.Printf("   表②(X×新客→缺口): action=%s rate=%.0fbps hits=%v\n", pipe2.Action, pipe2.RateBps, pipe2.Hits)
}

// demoDecisionTree 独立演示决策树: 挂上第一部分的追踪监听器,
// 看"冲突集永远只有一片叶"——互斥来自路径划分, 不靠 salience。
// (tracer 的 result 传占位 RiskResult, 手法同 demoAllVerbs: 冲突集行
// 完整可用, 执行前/后快照的 score/decision 是占位零值, 忽略即可)
func demoDecisionTree(zlog *zap.Logger) {
	fmt.Println("\n═══ ② 决策树: 客群评估(挂追踪器看'冲突集只有一片叶') ═══")
	lib := buildKnowledgeLibrary("tree", "1.0.0", grlDecisionTree)

	// 新客 25 岁, 设备分 62(存疑) → 应落叶4: C/10000
	pipe := &Pipe{} // Score=0(<40), Grade="" 待树写入
	tracer := &CycleTraceListener{zlog: zlog, result: &RiskResult{}}
	runPipeKB(lib, "tree", &Transaction{}, &Portrait{IsNew: true, Age: 25, DeviceScore: 62}, pipe, tracer)
	fmt.Printf("   树(新客25岁/设备62): grade=%s limit=%.0f hits=%v\n", pipe.Grade, pipe.Limit, pipe.Hits)
}

// flowNode 决策流节点: 名字 + 独立知识库。
// 对照第一部分: 那里的"抢占/打分/标定/兜底"是同一知识库里的 salience 分层;
// 这里升级为节点边界 —— 每个节点可独立热更新、独立观测、独立短路。
type flowNode struct {
	name string
	lib  *ast.KnowledgeLibrary
	kb   string
}

// runFlow 决策流编排器: 顺序执行节点, 节点间靠同一个 *Pipe 传数据,
// 每个节点跑完检查流级短路信号 Pipe.Blocked。
func runFlow(label string, nodes []flowNode, txn *Transaction, pt *Portrait, pipe *Pipe) {
	fmt.Printf("\n━━━━━━ %s ━━━━━━\n", label)
	track := make([]string, 0, len(nodes))
	for _, n := range nodes {
		runPipeKB(n.lib, n.kb, txn, pt, pipe, nil)
		if pipe.Blocked { // ★ 决策流执行短路
			track = append(track, n.name+"(⛔BLOCK)")
			fmt.Printf("   ⛔ 流级短路: 节点[%s] 置 Blocked → 下游节点全部跳过\n", n.name)
			break
		}
		track = append(track, n.name)
	}
	fmt.Printf("   轨迹: start → %s → end\n", strings.Join(track, " → "))
	fmt.Printf("   终局: score=%.0f grade=%s limit=%.0f action=%s rate=%.0fbps blocked=%t\n",
		pipe.Score, pipe.Grade, pipe.Limit, pipe.Action, pipe.RateBps, pipe.Blocked)
	fmt.Printf("   证据: %v\n", pipe.Hits)
}

// demoDecisionFlow 决策流: 打分规则集 → 决策树 → 决策表 三节点串联,
// 数据总线 Pipe 贯穿, 场景A 演示流级短路。
func demoDecisionFlow(zlog *zap.Logger) {
	fmt.Println("\n═══ ④ 决策流: 打分规则集 → 决策树 → 决策表 ═══")

	nodes := []flowNode{
		{name: "打分规则集", lib: buildKnowledgeLibrary("score", "1.0.0", grlScoreNode), kb: "score"},
		{name: "决策树·客群评估", lib: buildKnowledgeLibrary("tree", "1.0.0", grlDecisionTree), kb: "tree"},
		{name: "决策表·交叉定价", lib: buildKnowledgeLibrary("table", "1.0.0", grlDecisionTable), kb: "table"},
	}

	// 流场景A: 高危交易(与第一部分场景1同数据) → 80+45=125 ≥ 100
	// → 节点① Complete+Blocked → 树/表未执行(轨迹里看不到它们)
	runFlow("流场景A: 高危交易(节点①短路)", nodes,
		&Transaction{Inflow24h: 800000, OutflowRatio: 0.95, NightRatio: 0.82, TxnCount24h: 120},
		&Portrait{IsNew: true, Age: 30},
		&Pipe{Threshold: 100})

	// 流场景B: 干净的新客 20 岁 → 打分 0 分 → 树叶2(新客年轻→C/8000)
	// → 表 C×新客 → 小额试用 120bps
	runFlow("流场景B: 新客年轻(全链路→C×新客)", nodes,
		&Transaction{Amount: 3000},
		&Portrait{IsNew: true, Age: 20, Income: 8000, DeviceScore: 85},
		&Pipe{Threshold: 100})

	// 流场景C: 干净的老客高收入 → 树叶5(A/50000) → 表 A×老客 → 提额邀约 30bps
	runFlow("流场景C: 老客高收入(全链路→A×老客)", nodes,
		&Transaction{Amount: 5000},
		&Portrait{IsNew: false, Age: 41, Income: 30000, DeviceScore: 90},
		&Pipe{Threshold: 100})
}

// ============================================================
// ★ 第三部分: GRL 高级语法 —— SQL 式条件的 grule 对照写法 ★
//
// 常有人拿 SQL/别家规则引擎的条件语法(in / like / contains / matches)
// 来问 grule 怎么写 —— GRL 没有这些关键字, 对应能力全部长在【方法】上:
// 字符串字段(接收者)自带一组内置方法, 切片/map 有 Len/Append,
// 剩下的缺口用 fact 自定义方法补。下表即本节 16 条规则(SYN-01~16)的目录:
//
//	SQL / 别家引擎写法           GRL 对照写法                          规则
//	─────────────────────────────────────────────────────────────────
//	Country IN ('CN','US')      Cust.Country.In("CN","US")           SYN-01
//	NOT IN (...)                !Cust.Country.In(...)                SYN-02
//	Email LIKE '%gmail%'        Cust.Email.Contains("gmail")         SYN-03
//	LIKE 'vip_%' / '%.com'      HasPrefix("vip_") / HasSuffix(...)   SYN-04
//	NOT LIKE 'test%'            !Cust.Name.HasPrefix("test")         SYN-05
//	REGEXP '^...$'              Cust.Email.MatchString("^...$")      SYN-06
//	Roles contains 'admin'      ContainsStr(Cust.Roles,"admin") 内置  SYN-07
//	  (数组/集合成员)             或自定义方法 Cust.HasRole("admin")
//	LENGTH(x)                   str/slice/map 皆有 .Len()             SYN-08
//	下标 / 键访问                Cust.Tags[0] / Cust.Limits["daily"]  SYN-08/09
//	嵌套列(User.Address.City)    Cust.Address.City(点号任意深度)        SYN-10
//	TRIM/UPPER/LOWER/比较        Trim()/ToUpper()/ToLower()/Compare   SYN-11
//	AND / OR / NOT              && / || / !(支持括号组合)              SYN-12
//	MAX/MIN/SUM                 Max()/Min() 内置; Sum 无内置→自定义     SYN-13
//	SUBSTRING_INDEX / REPLACE   Split("@")[1] / Replace("-","") /    SYN-14
//	  / 子串计数                 Count("@") —— Split 结果可再下标/.Len()
//	map 动态键 / 值参与算术      Limits[Cust.Tier](键=另一字段) /       SYN-15
//	                            Limits["single"]*2 <= Limits["daily"]
//	map 值下钻 / 值接方法链      Sites["hq"].City(map→struct) /       SYN-16
//	                            Perms["pay"][0](map→切片) /
//	                            Ext["channel"].ToUpper()(map→字符串)
//
// 命名提醒: 别家引擎的 contains()/startsWith()/endsWith() 在 grule 里
// 叫 Contains/HasPrefix/HasSuffix —— 方法名照搬 Go strings 包。
// 字符串接收者方法全集(v1.20.4 源码 model/GoDataAccessLayer.go CallFunction):
//	In / Compare / Contains / Count / HasPrefix / HasSuffix / Index /
//	LastIndex / Repeat / Replace / Split / ToLower / ToUpper / Trim /
//	Len / MatchString(正则);
// 切片接收者: Len / Append; map 接收者: Len;
// 数字/布尔接收者: 无方法(数学处理走 Max/Min/Abs/Ceil/Floor/Round/... 内置函数)。
//
// 自定义"函数"的形态(Method/Function 调用, 第一部分已覆盖, 此处只点破机制):
// grule 没有注册全局函数的入口, 自定义逻辑一律挂成 fact 的导出方法 ——
// HitRec/Evidence/SetRiskLevel 是第一部分示范, 本节 HasRole/SumAmts 是
// "当函数用"的新例子; when/then 里都可调用, 唯一红线是第一部分的缓存纪律
// (方法改了被 when 引用的字段必须 Changed; 本节方法全是只读计算, 无此顾虑)。
//
// 纪律衔接(与第一部分完全一致): 本节 when 全部只依赖只读 Cust
// → 打分层同款 Retract 自我撤出; Res.Hits 只写不读 → 方法追加安全;
// 本知识库(syntax)独立于 risk, salience 只决定演示输出的先后次序。
// ============================================================

// CustAddress 客户地址 —— 嵌套 fact 演示专用(SYN-10):
// GRL 用点号一路下钻 Cust.Address.City, 深度不限, 反射约束不变(全链路导出字段)
type CustAddress struct {
	Country string // 归属国 | 被引用: SYN-10
	City    string // 城市   | 被引用: SYN-10(嵌套字段上照样能接方法链 City.In(...))
}

// Customer KYC 客户档案 —— 第三部分只读输入 fact(13 条规则的 when 只依赖它)
type Customer struct {
	Name      string                 // 登录名 | SYN-04 HasPrefix / SYN-05 NOT LIKE / SYN-11 链式+Compare
	Nick      string                 // 昵称(两端故意留空格) | SYN-11 Trim
	Email     string                 // 邮箱 | SYN-03 Contains / SYN-04 HasSuffix / SYN-06 正则 / SYN-08 Len
	Country   string                 // 国家码 | SYN-01 In / SYN-02 NOT IN / SYN-11 ToLower / SYN-12 ||
	Roles     []string               // 角色数组 | SYN-07 成员判定(内置+自定义两种形态) / SYN-08 Len
	Tags      []string               // 标签数组 | SYN-08 下标 Tags[0] / SYN-12 取反组合
	Limits    map[string]float64     // 额度表 | SYN-09 键访问 Limits["daily"] / SYN-13 Max 参数
	Address   CustAddress            // 嵌套结构体 | SYN-10
	MonthAmts []float64              // 近3月消费 | SYN-13 SumAmts 聚合
	Phone     string                 // 手机号(带横线) | SYN-14 Replace 归一化后校验位数
	Tier      string                 // 客户档位(daily/single) | SYN-15 作 Limits 的【动态键】
	Sites     map[string]CustAddress // 站点表 | SYN-16 map值→struct 继续点号下钻
	Perms     map[string][]string    // 权限表 | SYN-16 map值→切片 继续 Len()/下标
	Ext       map[string]string      // 扩展属性 | SYN-16 map值→字符串 继续接方法链
}

// HasRole 数组成员判定的【自定义方法】形态(SYN-07)。
// 与内置函数 ContainsStr(Cust.Roles, x) 等价(内部同为 slices.Contains),
// 两种形态在 SYN-07 同场对照; 内置版仅支持 []string,
// 其他元素类型的切片就得靠这种方法包一层。
func (c *Customer) HasRole(role string) bool {
	return slices.Contains(c.Roles, role)
}

// SumAmts 消费合计(SYN-13)。GRL 内置数学函数一大族(Max/Min/Abs/Sqrt/...)
// 却没有 Sum 聚合 —— SQL SUM() 的对应物只能自定义方法补位(同 CalculateTotal 手法)。
func (c *Customer) SumAmts() float64 {
	sum := 0.0
	for _, v := range c.MonthAmts {
		sum += v
	}
	return sum
}

// MatchResult 第三部分输出 fact。
// Hits 只写不读 → Hit 方法追加安全(第一部分纪律); Score 仅 SYN-13 的
// then 赋值写入、不被任何 when 引用 —— 赋值语句是最省心的写法。
type MatchResult struct {
	Score float64  // SYN-13 的 Max/Min/Sum 聚合演算结果
	Hits  []string // 命中清单: 每条 = 一种"SQL 概念 → GRL 写法"的对照记录
}

// Hit 追加命中记录(同 Pipe.Hit / RiskResult.HitRec 的只写纪律)
func (m *MatchResult) Hit(s string) { m.Hits = append(m.Hits, s) }

// grlAdvancedSyntax 16 条语法对照规则: 一条规则演示一种 SQL 式写法
// (SYN-14~16 是字符串加工与 map 进阶的补充篇),
// when 全部只依赖只读 Cust → 打分层同款 Retract 防重
const grlAdvancedSyntax = `
// SYN-01 | SQL: Country IN ('CN','US','JP') —— GRL 无 in 关键字,
// 用字符串接收者的内置方法 In(a,b,...): 接收者等于任一参数即为真
rule SynIn "SYN-01 IN 白名单国家" salience 130 {
    when
        Cust.Country.In("CN", "US", "JP")
    then
        LogFormat("SYN-01 IN: Country=%v 在白名单内", Cust.Country);
        Res.Hit("SYN-01 IN           → Country.In(CN,US,JP)");
        Retract("SynIn");
}

// SYN-02 | SQL: NOT IN —— 同样没有关键字, ! 取反 In() 即可;
// 顺带演示单目 ! 直接作用于方法调用原子(括号形态 !(...) 见 SYN-12)
rule SynNotIn "SYN-02 NOT IN 制裁名单之外" salience 125 {
    when
        !Cust.Country.In("KP", "IR", "SY")
    then
        LogFormat("SYN-02 NOT IN: Country=%v 不在制裁名单", Cust.Country);
        Res.Hit("SYN-02 NOT IN       → !Country.In(KP,IR,SY)");
        Retract("SynNotIn");
}

// SYN-03 | SQL: Email LIKE '%gmail%' —— 子串匹配 = Contains
// (函数形态的等价物: 内置 StringContains(Cust.Email, "gmail"))
rule SynLike "SYN-03 LIKE 子串匹配" salience 120 {
    when
        Cust.Email.Contains("gmail")
    then
        LogFormat("SYN-03 LIKE %%x%%: Email=%v 含 gmail", Cust.Email);
        Res.Hit("SYN-03 LIKE '%x%'   → Email.Contains(gmail)");
        Retract("SynLike");
}

// SYN-04 | SQL: LIKE 'vip_%'(前缀) / LIKE '%@gmail.com'(后缀) ——
// 别家引擎的 startsWith/endsWith 在 grule 叫 HasPrefix/HasSuffix
rule SynPrefixSuffix "SYN-04 LIKE 前缀/后缀" salience 115 {
    when
        Cust.Name.HasPrefix("vip_") && Cust.Email.HasSuffix("@gmail.com")
    then
        LogFormat("SYN-04 前后缀: Name=%v 以 vip_ 开头且邮箱以 @gmail.com 结尾", Cust.Name);
        Res.Hit("SYN-04 LIKE 'x%/%x' → Name.HasPrefix(vip_) && Email.HasSuffix(@gmail.com)");
        Retract("SynPrefixSuffix");
}

// SYN-05 | SQL: Name NOT LIKE 'test%' —— ! + HasPrefix;
// 反例档案(test_bob)正是被这条 ! 挡在门外的
rule SynNotLike "SYN-05 NOT LIKE 排除测试账号" salience 110 {
    when
        !Cust.Name.HasPrefix("test")
    then
        LogFormat("SYN-05 NOT LIKE: Name=%v 不是 test 开头", Cust.Name);
        Res.Hit("SYN-05 NOT LIKE     → !Name.HasPrefix(test)");
        Retract("SynNotLike");
}

// SYN-06 | SQL: REGEXP / 别家的 matches —— MatchString(Go regexp 语法);
// 模式里用字符类 [.] 代替 \. 转义, 免得在 GRL 字符串里再嵌一层反斜杠;
// ⚠ 模式非法不报编译错, when 求值出错 → 规则被静默跳过(第一部分讲过的行为)
rule SynRegex "SYN-06 正则匹配" salience 105 {
    when
        Cust.Email.MatchString("^[a-z0-9_]+@gmail[.]com$")
    then
        LogFormat("SYN-06 正则: Email=%v 匹配 ^[a-z0-9_]+@gmail[.]com$", Cust.Email);
        Res.Hit("SYN-06 REGEXP       → Email.MatchString(^[a-z0-9_]+@gmail[.]com$)");
        Retract("SynRegex");
}

// SYN-07 | 数组/集合成员判定("Roles contains admin")的两种形态同场对照:
//   内置函数 ContainsStr(切片, 值) —— 仅 []string;
//   自定义方法 Cust.HasRole(值)    —— 万能兜底, 任意切片类型都能包
// (字符串的 .Contains 是子串匹配, 别拿去判成员; string.In 的接收者也只能是字符串)
rule SynArrContains "SYN-07 数组成员判定" salience 100 {
    when
        ContainsStr(Cust.Roles, "admin") && Cust.HasRole("auditor")
    then
        Log("SYN-07 数组成员: Roles 同时含 admin(内置 ContainsStr) 与 auditor(自定义 HasRole)");
        Res.Hit("SYN-07 数组contains  → ContainsStr(Roles,admin) / HasRole(auditor)");
        Retract("SynArrContains");
}

// SYN-08 | LENGTH() 与下标: 字符串/切片/map 都有内置 .Len()(返回整数,
// 与整数字面量比较, 同 int64 字段的规矩); 切片下标 Tags[0] 从 0 起,
// 越界会在 when 求值报错 → 规则跳过, 注入前保证数据形状
rule SynLenIndex "SYN-08 长度与数组下标" salience 95 {
    when
        Cust.Email.Len() > 10 && Cust.Roles.Len() >= 2 && Cust.Tags[0] == "vip"
    then
        LogFormat("SYN-08 Len/下标: Email 长度=%v(>10), 且 Tags[0]==vip", Cust.Email.Len());
        Res.Hit("SYN-08 LENGTH/下标  → Email.Len()>10 && Roles.Len()>=2 && Tags[0]==vip");
        Retract("SynLenIndex");
}

// SYN-09 | map 键访问: Limits["daily"] 取出的值直接参与比较/运算/传参,
// map 接收者同样有 .Len(); 键不存在没有编译期保护, 注入时保证键齐全
rule SynMapAccess "SYN-09 Map 键访问" salience 90 {
    when
        Cust.Limits["daily"] >= 10000.0 && Cust.Limits.Len() == 2
    then
        LogFormat("SYN-09 Map: 日限额 Limits[daily]=%v ≥ 10000", Cust.Limits["daily"]);
        Res.Hit("SYN-09 Map 键       → Limits[daily]>=10000 && Limits.Len()==2");
        Retract("SynMapAccess");
}

// SYN-10 | 嵌套属性访问: 点号一路下钻(User.Address.City 式), 深度不限,
// 嵌套字段上照样能接内置方法链(City.In(...))
rule SynNested "SYN-10 嵌套属性访问" salience 85 {
    when
        Cust.Address.Country == "CN" && Cust.Address.City.In("Shanghai", "Beijing", "Shenzhen")
    then
        LogFormat("SYN-10 嵌套: Address.City=%v 命中一线城市名单", Cust.Address.City);
        Res.Hit("SYN-10 嵌套属性     → Address.Country==CN && Address.City.In(...)");
        Retract("SynNested");
}

// SYN-11 | 字符串处理函数与【链式调用】: Trim/ToUpper/ToLower 返回新串,
// 可继续点出下一个方法(Name.ToUpper().HasPrefix(...)); Compare 返回
// -1/0/1(对应 SQL 的 </=/>) —— 返回整数, 与整数字面量 0 比较
rule SynStrFuncs "SYN-11 字符串函数与链式调用" salience 80 {
    when
        Cust.Nick.Trim() == "alice" &&
        Cust.Name.ToUpper().HasPrefix("VIP_") &&
        Cust.Country.ToLower() == "cn" &&
        Cust.Name.Compare("vip_alice") == 0
    then
        LogFormat("SYN-11 字符串函数: Trim 后昵称=%v, 链式 ToUpper→HasPrefix 通过", Cust.Nick.Trim());
        Res.Hit("SYN-11 TRIM/UPPER/LOWER/COMPARE → 链式 Name.ToUpper().HasPrefix(VIP_)");
        Retract("SynStrFuncs");
}

// SYN-12 | 复杂条件组合: && || ! 加圆括号随意嵌套 ——
// !(...) 括号取反与 !原子 取反(SYN-02/05 用过)两种形态都合法
rule SynComplexBool "SYN-12 复杂布尔组合" salience 75 {
    when
        (Cust.Country == "CN" || Cust.Country == "US") &&
        !(Cust.Tags[0] == "blacklist") &&
        !Cust.HasRole("banned")
    then
        LogFormat("SYN-12 组合: (CN||US) 为真且无黑名单标签/封禁角色, Country=%v", Cust.Country);
        Res.Hit("SYN-12 &&·||·!      → (Country==CN||US) && !(Tags[0]==blacklist) && !HasRole(banned)");
        Retract("SynComplexBool");
}

// SYN-13 | 聚合: Max/Min 是内置函数(变参 float64, 同族还有 Abs/Ceil/
// Floor/Sqrt/Round/Pow...); SUM 没有内置 —— 自定义方法 SumAmts() 补位。
// then 里演示聚合值参与赋值运算(Score 不被任何 when 引用, 赋值即可)
rule SynMathAgg "SYN-13 聚合 Max/Min/Sum" salience 70 {
    when
        Cust.SumAmts() > 30000.0 && Max(Cust.Limits["daily"], Cust.Limits["single"]) >= 50000.0
    then
        Res.Score = Min(Cust.SumAmts() / 1000.0, 99.0);
        LogFormat("SYN-13 聚合: 近3月消费合计=%v(自定义 Sum), 聚合分=Min(合计/1000, 99)", Cust.SumAmts());
        Res.Hit("SYN-13 MAX/MIN/SUM  → Max(Limits内置) + SumAmts()自定义 + Min()封顶");
        Retract("SynMathAgg");
}

// SYN-14 | 字符串加工三件套: Split(切分)/Replace(替换)/Count(子串计数) ——
// SQL 对应 SUBSTRING_INDEX / REPLACE / (无直接对应)。
// 要点是【方法返回什么类型, 后面就能接什么操作】:
//   Split 返回切片 → 可再接 [1] 下标取段、可再接 .Len() 验段数;
//   Replace 返回新串 → 可再接 .Len() 校验归一化后的位数
rule SynStrTransform "SYN-14 Split/Replace/Count 加工" salience 65 {
    when
        Cust.Email.Split("@")[1] == "gmail.com" &&
        Cust.Email.Split("@").Len() == 2 &&
        Cust.Email.Count("@") == 1 &&
        Cust.Phone.Replace("-", "").Len() == 11
    then
        LogFormat("SYN-14 加工: 域名=%v(Split 取段), 手机号 Replace 去横线后恰 11 位", Cust.Email.Split("@")[1]);
        Res.Hit("SYN-14 SPLIT/REPLACE/COUNT → Split(@)[1]==gmail.com && Replace(-,).Len()==11 && Count(@)==1");
        Retract("SynStrTransform");
}

// SYN-15 | map 进阶①: 键不必写死 —— 【动态键】可以是表达式/另一 fact 字段
// (Limits[Cust.Tier]: 客户在哪个档位, 就取哪个档位的额度 —— 配置驱动阈值
// 的常用形态); 取出的值就是数值, 直接参与算术与比较(SYN-09 是字面量键的基础版)
rule SynMapDynKey "SYN-15 Map 动态键与值算术" salience 60 {
    when
        Cust.Limits[Cust.Tier] >= 30000.0 &&
        Cust.Limits["single"] * 2.0 <= Cust.Limits["daily"]
    then
        LogFormat("SYN-15 Map动态键: Limits[Tier=%v] ≥ 30000, 且 single*2 ≤ daily", Cust.Tier);
        Res.Hit("SYN-15 Map 动态键   → Limits[Cust.Tier]>=30000 && Limits[single]*2<=Limits[daily]");
        Retract("SynMapDynKey");
}

// SYN-16 | map 进阶②: 值不止标量, 取出后按其类型继续走完整语法 ——
//   map→struct : Sites["hq"].City 继续点号下钻(再嵌一层 SYN-10);
//   map→切片   : Perms["pay"].Len() / Perms["pay"][0] 继续 Len 与下标;
//   map→字符串 : Ext["channel"].ToUpper() 继续接字符串方法链。
// 注意: 三层组合里任何一个键缺失都是 when 求值错误 → 规则被静默跳过,
// 复合结构的 fact 注入前更要保证形状完整
rule SynMapDrill "SYN-16 Map 值下钻与方法链" salience 55 {
    when
        Cust.Sites["hq"].City == "Shanghai" &&
        Cust.Perms["pay"].Len() >= 2 &&
        Cust.Perms["pay"][0] == "query" &&
        Cust.Ext["channel"].ToUpper() == "APP"
    then
        LogFormat("SYN-16 Map下钻: 总部城市=%v, 支付权限≥2且首项query, 渠道大写==APP", Cust.Sites["hq"].City);
        Res.Hit("SYN-16 Map 下钻/链  → Sites[hq].City && Perms[pay][0]==query && Ext[channel].ToUpper()==APP");
        Retract("SynMapDrill");
}
`

// demoAdvancedSyntax 第三部分入口: 同一套 16 条语法规则跑两份档案对照 ——
// 语法A 档案按条件量身定制, 16 种写法全命中(每种各留一条对照记录);
// 语法B 反例档案只剩 SYN-02(NOT IN)成立: test_ 前缀被 SYN-05 的 ! 排除,
// 名单/前后缀/正则/嵌套/聚合/加工/map进阶全不匹配 —— 对照两份命中清单
// 看每种写法的判定边界。
// (不挂 tracer: 16 条规则逐 cycle 追踪太吵, 规则自带的 Log/LogFormat 足够)
func demoAdvancedSyntax(zlog *zap.Logger) {
	fmt.Println("\n═══ ★ 第三部分: GRL 高级语法(SQL 式条件对照, SYN-01~16) ═══")
	lib := buildKnowledgeLibrary("syntax", "1.0.0", grlAdvancedSyntax)

	runOnce := func(label string, cust *Customer) {
		kb, err := lib.NewKnowledgeBaseInstance("syntax", "1.0.0")
		if err != nil {
			log.Fatalf("获取知识库实例失败(syntax): %v", err)
		}
		res := &MatchResult{}
		dctx := ast.NewDataContext()
		if err := dctx.Add("Cust", cust); err != nil {
			log.Fatal(err)
		}
		if err := dctx.Add("Res", res); err != nil {
			log.Fatal(err)
		}

		fmt.Printf("\n━━━━━━ %s ━━━━━━\n", label)
		eng := engine.NewGruleEngine()
		eng.MaxCycle = 50
		if err := eng.Execute(dctx, kb); err != nil {
			log.Fatalf("推理失败(syntax): %v", err)
		}
		fmt.Printf("   命中 %d/16 种写法 | SYN-13 聚合分=%.0f\n", len(res.Hits), res.Score)
		for _, h := range res.Hits {
			fmt.Printf("   · %s\n", h)
		}
	}

	// 语法A: 每个字段都踩在 SYN-01~16 的条件上(见 Customer 字段注释的被引用清单)
	runOnce("语法A: 全命中档案(vip_alice)", &Customer{
		Name:      "vip_alice",                                         // SYN-04 前缀 / SYN-05 非test / SYN-11 链式
		Nick:      "  alice  ",                                         // SYN-11 Trim 后 == alice
		Email:     "vip_alice@gmail.com",                               // SYN-03/04/06/08 / SYN-14 Split 域名
		Country:   "CN",                                                // SYN-01/02/11/12
		Roles:     []string{"admin", "auditor"},                        // SYN-07 两种成员判定 / SYN-08 Len
		Tags:      []string{"vip", "2026"},                             // SYN-08 下标 / SYN-12 非黑名单
		Limits:    map[string]float64{"daily": 50000, "single": 20000}, // SYN-09 / SYN-13 Max / SYN-15 算术
		Address:   CustAddress{Country: "CN", City: "Shanghai"},        // SYN-10 嵌套
		MonthAmts: []float64{12000, 15000, 9000},                       // SYN-13 Sum=36000 → 聚合分36
		Phone:     "138-0013-8000",                                     // SYN-14 Replace 去横线后恰 11 位
		Tier:      "daily",                                             // SYN-15 动态键 → Limits[daily]=50000
		Sites: map[string]CustAddress{ // SYN-16 map→struct 下钻
			"hq":     {Country: "CN", City: "Shanghai"},
			"branch": {Country: "SG", City: "Singapore"},
		},
		Perms: map[string][]string{"pay": {"query", "refund"}},   // SYN-16 map→切片 Len/下标
		Ext:   map[string]string{"channel": "app", "lang": "zh"}, // SYN-16 map→字符串→ToUpper
	})

	// 语法B: 反例 —— 仅 SYN-02(不在制裁名单)成立, 其余 15 条全被挡:
	// test_ 前缀(SYN-05 取反排除)、非 gmail(03/04/06/14)、单角色(08)、
	// 黑名单标签(12)、低额度(09/13/15)、非 CN 国家/地址/站点(01/10/11/12/16)
	// —— 注意 B 的 map/切片键位全部齐备(值不达标而已): 缺键会变成 when
	// 求值错误→静默跳过, 那就不是"条件为假"而是"数据形状问题"了
	runOnce("语法B: 反例档案(test_bob, 仅 NOT IN 命中)", &Customer{
		Name:      "test_bob",
		Nick:      "bob",
		Email:     "bob-99@corp-mail.io",
		Country:   "SG",
		Roles:     []string{"viewer"},
		Tags:      []string{"blacklist"},
		Limits:    map[string]float64{"daily": 1000, "single": 500},
		Address:   CustAddress{Country: "SG", City: "Singapore"},
		MonthAmts: []float64{100, 200},
		Phone:     "65-6123-456", // Replace 后 9 位 ≠ 11 → SYN-14 ✗
		Tier:      "single",      // Limits[single]=500 < 30000 → SYN-15 ✗
		Sites:     map[string]CustAddress{"hq": {Country: "SG", City: "Singapore"}},
		Perms:     map[string][]string{"pay": {"query"}},
		Ext:       map[string]string{"channel": "web"},
	})
}

func main() {
	// 观测接入点 A: 官方日志替换 API(见文件头注解: v1.15 内部包
	// init 时已捕获 logrus, 此调用对内部日志实际不生效, 保留作演示)
	zlog := newDemoLogger()
	defer zlog.Sync()
	grulelogger.SetLogger(zlog)
	// 观测接入点 C: GRL 内置 Log()/LogFormat() 走 ast 包的 GrlLogger,
	// 必须单独注入, 否则规则 then 里的打印语句不会有任何输出
	ast.SetLogger(zlog)

	lib := buildKnowledgeLibrary("risk", "1.0.0", grl)

	fmt.Println("═══ grule 风控示例 (阈值 100) ═══")

	// 场景1: 洗钱高危 → AML-001(+80) + AML-002(+45) = 125 ≥ 100
	//        → CTRL-001 抢占短路 → 拦截; 兜底层一次都没执行
	evaluate(zlog, lib, "场景1: 高危(快进快出+夜间高频)", &Transaction{
		Inflow24h: 800000, OutflowRatio: 0.95,
		NightRatio: 0.82, TxnCount24h: 120,
	})

	// 场景2: PAY-001(+40) → GRADE-001 标定 HIGH(方法+Changed, 自我证伪
	//        退出冲突集) → CTRL-002 聚合决策转人工
	//        (40 恰在边界: [40,阈值) 归人工, 边界含左端点)
	evaluate(zlog, lib, "场景2: 中风险(新设备大额)", &Transaction{
		IsNewDevice: true, Amount: 60000,
	})

	// 场景3: 零命中 → Score=0 → CTRL-003 放行, 两个 cycle 收敛
	evaluate(zlog, lib, "场景3: 正常交易", &Transaction{
		Amount: 200,
	})

	// 场景4: 两条支付规则叠加(PAY-001 +40, PAY-002 +80) → 120 ≥ 100
	//        → 抢占层短路拦截; GRADE-001 两轮都在冲突集里陪跑,
	//        先输给 PAY-002(75) 再被 ThresholdBlock(1000) 碾压 + Complete
	evaluate(zlog, lib, "场景4: 超大额叠加(触发短路)", &Transaction{
		IsNewDevice: true, Amount: 600000,
	})

	// 三动词组合演示(用户格式的 ProcessOrder + AuditTotal 观察者, A/B 对比)
	// demoAllVerbs(zlog)

	// demoInfiniteLoop(zlog)

	// ★ 第二部分: 决策表 / 决策树 / 决策图 / 决策流(见上方"★ 第二部分"注解块)
	demoDecisionTable(zlog)
	demoDecisionTree(zlog)
	demoDecisionGraph()
	demoDecisionFlow(zlog)

	// ★ 第三部分: GRL 高级语法 —— SQL 式条件对照(见"★ 第三部分"注解块)
	demoAdvancedSyntax(zlog)
}

// zen-engine 对照示例 —— 把 3main.go 的 grule 风控推理转成 GoRules Zen 决策图
// (github.com/gorules/zen-go, Rust 内核 zen-engine 的 CGO 绑定)
//
// ═══ 深入分析: grule(3main.go) → zen 的范式转换 ═══
//
// grule 是【前向链推理】: 规则共享一块工作内存, 每个 cycle 重新求值全部 when,
// 冲突消解选一条执行, 数据变更再驱动下一轮 —— 因此才需要那三样防重手法
// (Retract / 守卫字段 / Changed) 和 MaxCycle 保险。
// zen 是【无环数据流(DAG)】: 决策是一张 JSON 图(JDM), 节点是纯函数,
// 按拓扑序单遍执行, 没有工作内存、没有 cycle、没有缓存失效问题 ——
// grule 里最难教的那些坑, 在这个范式下整个不存在。
//
// 3main.go 构件 → 本文件 JDM 的逐项映射:
//
//	grule(3main.go)                          zen(9main.go)
//	───────────────────────────────────────────────────────────────────────
//	DataContext.Add("Txn"/"Result")          Evaluate 的 context JSON {"txn":…,"threshold":…}
//	打分层 4 条规则(可同时命中, 累分)          决策表 hitPolicy="collect": 命中行全收集
//	  └ Retract 防重                          不需要 —— 单遍求值, 无第二轮
//	  └ 累分必须用赋值语句(缓存失效)            不需要 —— sum() 在下游节点纯函数计算
//	HitRec 证据链(规则ID/分值/证据快照)         collect 行输出 {ruleId, ruleName, score, evidence: txn}
//	Σ累分 Result.Score                        表达式节点: sum(map(hits, #.score))
//	GRADE-001 标定(方法+Changed 自证伪)        一行三元表达式 score>=40 ? 'HIGH' : '-'
//	CTRL-001 抢占层 salience 1000 + Complete  switch 节点第 1 条语句(按序求值, 首真路由,
//	                                          其余分支根本不进 —— 这就是"短路")
//	CTRL-002/003 兜底层 Decision=="" 守卫     switch 后续语句/默认分支 —— 互斥天然成立, 无需守卫
//	阈值注入 Result.Threshold                 context.threshold, switch 条件 score >= threshold
//	热更新(指纹比对+atomic 换快照)             换一份 JDM JSON 重建 Decision(Rust 侧编译)
//	CycleTraceListener 逐 cycle 观测          EvaluationOptions{Trace:true} 逐节点 输入/输出/耗时
//	MaxCycle 防死循环                         无 cycle 可防; MaxDepth 管子决策嵌套深度
//
// 一句话: grule 的"冲突消解"在 zen 里退化成两处静态次序 ——
// 表内的 hitPolicy(first/collect) 和 switch 的语句排列; 推理变成了数据流。
//
// ⚠ 一处范式导致的语义差异(实测, 非 bug):
// 拦截场景(1/4)的 riskLevel —— grule 版是 "-": GRADE-001 作为规则在冲突集
// 里陪跑, 先输给打分层、再被 Complete() 连同兜底层一起掐掉, 从未执行;
// zen 版是 "HIGH": 标定是 switch 上游的纯函数节点, 短路发生在它之后,
// 必然求值。DAG 里没有"规则被饿死"这回事 —— 拦截件也带等级, 业务上通常
// 更合理; 但迁移时要意识到, grule 依赖执行时序的"从未执行"语义不会自动保留。
//
// 运行: go run ./examples/9main.go
// (依赖 CGO: zen-go 自带各平台预编译静态库, darwin/arm64 开箱即用)

//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"

	zen "github.com/gorules/zen-go"
)

// riskJDM —— 决策图(JDM, JSON Decision Model)。生产中这份 JSON 由 GoRules
// 可视化编辑器(BRMS)产出、按 key 经 Loader 加载; 这里内联以保持单文件演示。
//
// 图结构(拓扑序即执行序):
//
//	input → 打分层(决策表 collect) → 聚合标定(表达式) → 决策路由(switch)
//	                                                    ├ s1: score>=threshold → 拦截分支 ┐
//	                                                    ├ s2: score>=40        → 人工分支 ┼→ output
//	                                                    └ s3: (默认)           → 放行分支 ┘
//
// 决策表列: 输入=6 个特征字段(txn.*), 输出=4 列(ruleId/ruleName/score/evidence);
// 空单元格 = 不约束该列(对应 grule 里 when 不引用该字段)。
// 单元格是 ZEL 一元测试("> 500000"), 输出格是 ZEL 表达式(数字/单引号串/txn 整体)。
const riskJDM = `{
  "contentType": "application/vnd.gorules.decision",
  "nodes": [
    { "id": "input1", "type": "inputNode", "name": "Request", "position": { "x": 0, "y": 0 } },
    {
      "id": "tableScore", "type": "decisionTableNode", "name": "打分层(collect全命中)",
      "position": { "x": 200, "y": 0 },
      "content": {
        "hitPolicy": "collect",
        "passThrough": true,
        "outputPath": "hits",
        "inputs": [
          { "id": "i1", "name": "24h流入",   "field": "txn.inflow24h" },
          { "id": "i2", "name": "流出比",    "field": "txn.outflowRatio" },
          { "id": "i3", "name": "夜间占比",  "field": "txn.nightRatio" },
          { "id": "i4", "name": "24h笔数",   "field": "txn.txnCount24h" },
          { "id": "i5", "name": "新设备",    "field": "txn.isNewDevice" },
          { "id": "i6", "name": "金额",      "field": "txn.amount" }
        ],
        "outputs": [
          { "id": "o1", "name": "规则ID",   "field": "ruleId" },
          { "id": "o2", "name": "规则名",   "field": "ruleName" },
          { "id": "o3", "name": "分值",     "field": "score" },
          { "id": "o4", "name": "证据快照", "field": "evidence" }
        ],
        "rules": [
          { "_id": "r1", "i1": "> 500000", "i2": "> 0.9", "i3": "", "i4": "", "i5": "", "i6": "",
            "o1": "1001", "o2": "'大额快进快出'",   "o3": "80", "o4": "txn" },
          { "_id": "r2", "i1": "", "i2": "", "i3": "> 0.7", "i4": "> 50", "i5": "", "i6": "",
            "o1": "1002", "o2": "'夜间高频交易'",   "o3": "45", "o4": "txn" },
          { "_id": "r3", "i1": "", "i2": "", "i3": "", "i4": "", "i5": "true", "i6": "> 50000",
            "o1": "2001", "o2": "'新设备大额支付'", "o3": "40", "o4": "txn" },
          { "_id": "r4", "i1": "", "i2": "", "i3": "", "i4": "", "i5": "", "i6": "> 500000",
            "o1": "2002", "o2": "'超大额交易'",     "o3": "80", "o4": "txn" }
        ]
      }
    },
    {
      "id": "exprAgg", "type": "expressionNode", "name": "聚合+标定",
      "position": { "x": 400, "y": 0 },
      "content": {
        "expressions": [
          { "id": "a1", "key": "txn",       "value": "txn" },
          { "id": "a2", "key": "threshold", "value": "threshold" },
          { "id": "a3", "key": "hits",      "value": "hits" },
          { "id": "a4", "key": "score",     "value": "sum(map(hits, #.score))" },
          { "id": "a5", "key": "riskLevel", "value": "sum(map(hits, #.score)) >= 40 ? 'HIGH' : '-'" }
        ]
      }
    },
    {
      "id": "switchDecision", "type": "switchNode", "name": "决策路由(首真短路)",
      "position": { "x": 600, "y": 0 },
      "content": {
        "hitPolicy": "first",
        "statements": [
          { "id": "s1", "condition": "score >= threshold" },
          { "id": "s2", "condition": "score >= 40" },
          { "id": "s3", "condition": "" }
        ]
      }
    },
    {
      "id": "exprBlock", "type": "expressionNode", "name": "分支-拦截",
      "position": { "x": 800, "y": -100 },
      "content": {
        "expressions": [
          { "id": "b1", "key": "decision",     "value": "'拦截/拒绝'" },
          { "id": "b2", "key": "ctrlRuleId",   "value": "4001" },
          { "id": "b3", "key": "ctrlRuleName", "value": "'阈值短路拦截'" },
          { "id": "b4", "key": "score",        "value": "score" },
          { "id": "b5", "key": "riskLevel",    "value": "riskLevel" },
          { "id": "b6", "key": "hits",         "value": "hits" }
        ]
      }
    },
    {
      "id": "exprManual", "type": "expressionNode", "name": "分支-转人工",
      "position": { "x": 800, "y": 0 },
      "content": {
        "expressions": [
          { "id": "m1", "key": "decision",     "value": "'人工审核'" },
          { "id": "m2", "key": "ctrlRuleId",   "value": "4002" },
          { "id": "m3", "key": "ctrlRuleName", "value": "'灰区转人工'" },
          { "id": "m4", "key": "score",        "value": "score" },
          { "id": "m5", "key": "riskLevel",    "value": "riskLevel" },
          { "id": "m6", "key": "hits",         "value": "hits" }
        ]
      }
    },
    {
      "id": "exprPass", "type": "expressionNode", "name": "分支-放行",
      "position": { "x": 800, "y": 100 },
      "content": {
        "expressions": [
          { "id": "p1", "key": "decision",     "value": "'通过'" },
          { "id": "p2", "key": "ctrlRuleId",   "value": "4003" },
          { "id": "p3", "key": "ctrlRuleName", "value": "'低分放行'" },
          { "id": "p4", "key": "score",        "value": "score" },
          { "id": "p5", "key": "riskLevel",    "value": "riskLevel" },
          { "id": "p6", "key": "hits",         "value": "hits" }
        ]
      }
    },
    { "id": "output1", "type": "outputNode", "name": "Response", "position": { "x": 1000, "y": 0 } }
  ],
  "edges": [
    { "id": "e1", "type": "edge", "sourceId": "input1",         "targetId": "tableScore" },
    { "id": "e2", "type": "edge", "sourceId": "tableScore",     "targetId": "exprAgg" },
    { "id": "e3", "type": "edge", "sourceId": "exprAgg",        "targetId": "switchDecision" },
    { "id": "e4", "type": "edge", "sourceId": "switchDecision", "targetId": "exprBlock",  "sourceHandle": "s1" },
    { "id": "e5", "type": "edge", "sourceId": "switchDecision", "targetId": "exprManual", "sourceHandle": "s2" },
    { "id": "e6", "type": "edge", "sourceId": "switchDecision", "targetId": "exprPass",   "sourceHandle": "s3" },
    { "id": "e7", "type": "edge", "sourceId": "exprBlock",      "targetId": "output1" },
    { "id": "e8", "type": "edge", "sourceId": "exprManual",     "targetId": "output1" },
    { "id": "e9", "type": "edge", "sourceId": "exprPass",       "targetId": "output1" }
  ]
}`

// zenHit 决策表 collect 的一行命中 —— 对应 3main.go 的 HitRecord
// (HitAt 打点/ResultSnap 属推理期状态, DAG 单遍执行没有中途状态, 由 Go 侧补时间戳)
type zenHit struct {
	RuleID   int64          `json:"ruleId"`
	RuleName string         `json:"ruleName"`
	Score    float64        `json:"score"`
	Evidence map[string]any `json:"evidence"`
}

// zenResult 决策图 output 节点的最终输出 —— 对应 3main.go 的 RiskResult
type zenResult struct {
	Score        float64  `json:"score"`
	RiskLevel    string   `json:"riskLevel"`
	Decision     string   `json:"decision"`
	CtrlRuleID   int64    `json:"ctrlRuleId"`
	CtrlRuleName string   `json:"ctrlRuleName"`
	Hits         []zenHit `json:"hits"`
}

// zenTraceNode Trace 里单个节点的执行记录 —— 对应 CycleTraceListener 的快照,
// 只是维度从"cycle"换成了"节点": 走过哪些节点、各花多少时间。
// 短路在这里直接可见: 拦截场景的 trace 里只有 exprBlock, 没有 exprManual/exprPass。
type zenTraceNode struct {
	Name        string `json:"name"`
	Performance string `json:"performance"`
	Order       int    `json:"order"`
}

func evaluateZen(decision zen.Decision, label string, txn map[string]any) {
	fmt.Printf("\n━━━━━━ %s ━━━━━━\n", label)

	// 对应 3main.go: dctx.Add("Txn", txn) + RiskResult{Threshold: 100}
	resp, err := decision.EvaluateWithOpts(map[string]any{
		"txn": txn, "threshold": 100,
	}, zen.EvaluationOptions{Trace: true, MaxDepth: 10})
	if err != nil {
		log.Fatalf("决策评估失败: %v", err)
	}

	var r zenResult
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		log.Fatalf("结果解析失败: %v\n原始: %s", err, string(resp.Result))
	}

	// 执行轨迹: 按 order 排序打印节点名+耗时 —— switch 短路一目了然
	if resp.Trace != nil {
		var traces map[string]zenTraceNode
		if err := json.Unmarshal(*resp.Trace, &traces); err == nil {
			nodes := make([]zenTraceNode, 0, len(traces))
			for _, t := range traces {
				nodes = append(nodes, t)
			}
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].Order < nodes[j].Order })
			fmt.Printf("   轨迹:")
			for i, n := range nodes {
				if i > 0 {
					fmt.Printf(" →")
				}
				fmt.Printf(" %s(%s)", n.Name, n.Performance)
			}
			fmt.Printf("  | 总耗时 %s\n", resp.Performance)
		}
	}

	fmt.Printf("   总分: %.0f | 等级: %s | 决策: %s\n", r.Score, r.RiskLevel, r.Decision)
	for _, h := range r.Hits {
		fmt.Printf("   命中: %-6d %s %+3.0f分  [证据 amount=%v inflow24h=%v]\n",
			h.RuleID, h.RuleName, h.Score, h.Evidence["amount"], h.Evidence["inflow24h"])
	}
	fmt.Printf("   命中: %-6d %s  +0分  (决策留痕, 同 3main 的 CTRL 层 0 分记录)\n", r.CtrlRuleID, r.CtrlRuleName)

	// 分值对账 —— 原样移植 3main.go 的审计: Σhits[].score == score
	sum := 0.0
	for _, h := range r.Hits {
		sum += h.Score
	}
	if math.Abs(sum-r.Score) > 1e-9 {
		fmt.Printf("   ⚠ 分值对账不平: score=%.0f ≠ Σhits=%.0f\n", r.Score, sum)
	}
}

func main() {
	// Loader 用于按 key 加载 JDM(对应服务里的 file/oracle 规则源);
	// 单文件演示直接 CreateDecision, 不走 Loader
	engine := zen.NewEngine(zen.EngineConfig{})
	defer engine.Dispose()

	decision, err := engine.CreateDecision([]byte(riskJDM))
	if err != nil {
		log.Fatalf("JDM 编译失败: %v", err)
	}
	defer decision.Dispose()

	fmt.Println("═══ zen-engine 版风控决策(与 3main.go 同一套业务规则, 阈值 100) ═══")

	// 四个场景与 3main.go 完全同参, 期望同结果 —— 跨引擎回归:
	// 场景1: 1001(+80)+1002(+45)=125 ≥ 100 → 拦截
	evaluateZen(decision, "场景1: 高危(快进快出+夜间高频)", map[string]any{
		"amount": 0, "isNewDevice": false, "inflow24h": 800000,
		"outflowRatio": 0.95, "nightRatio": 0.82, "txnCount24h": 120,
	})
	// 场景2: 2001(+40) → 40∈[40,100) → 人工审核, 等级 HIGH
	evaluateZen(decision, "场景2: 中风险(新设备大额)", map[string]any{
		"amount": 60000, "isNewDevice": true, "inflow24h": 0,
		"outflowRatio": 0.0, "nightRatio": 0.0, "txnCount24h": 0,
	})
	// 场景3: 零命中 → 0 < 40 → 通过
	evaluateZen(decision, "场景3: 正常交易", map[string]any{
		"amount": 200, "isNewDevice": false, "inflow24h": 0,
		"outflowRatio": 0.0, "nightRatio": 0.0, "txnCount24h": 0,
	})
	// 场景4: 2001(+40)+2002(+80)=120 ≥ 100 → 拦截(两行同时命中 = collect 的价值)
	evaluateZen(decision, "场景4: 超大额叠加(触发短路)", map[string]any{
		"amount": 600000, "isNewDevice": true, "inflow24h": 0,
		"outflowRatio": 0.0, "nightRatio": 0.0, "txnCount24h": 0,
	})
}

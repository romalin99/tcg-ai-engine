// 实验D深挖: Reset("R.Score") 到底匹配到了什么 —— 两条代码路径的边界验证
package main

import (
	"fmt"
	"log"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	"github.com/hyperjumptech/grule-rule-engine/engine"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Txn struct{ Amount float64 }
type Res struct{ Score float64 }

func (r *Res) AddScore(s float64) {
	r.Score += s
	fmt.Printf("      [Go侧] AddScore 真调用! Score -> %.0f\n", r.Score)
}

func run(title, grl string) {
	fmt.Printf("\n════ %s ════\n", title)
	lib := ast.NewKnowledgeLibrary()
	if err := builder.NewRuleBuilder(lib).BuildRuleFromResource("lab", "1.0.0",
		pkg.NewBytesResource([]byte(grl))); err != nil {
		log.Fatal(err)
	}
	kb, _ := lib.NewKnowledgeBaseInstance("lab", "1.0.0")
	res := &Res{}
	dctx := ast.NewDataContext()
	dctx.Add("T", &Txn{Amount: 1})
	dctx.Add("R", res)
	eng := engine.NewGruleEngine()
	eng.MaxCycle = 4
	eng.Execute(dctx, kb)
	fmt.Printf("  MaxCycle=4 跑满后 Score = %.0f\n", res.Score)
}

func main() {
	// E-A: 子串匹配的宽松性 —— 传 "AddScore" 也能命中原子快照
	// 预测: "R.AddScore(1.0)" 含子串 "AddScore" → 每轮重置 → 4次真调用
	run(`E-A: Changed("AddScore") 宽松子串`, `
rule Loop "L" { when T.Amount > 0.0 then R.AddScore(1.0); Changed("AddScore"); }`)

	// E-B1: 让变量 R.Score 真实存在(出现在 when 里) + Changed("R.Score")
	// 预测: 第一段循环精确命中变量 → ResetVariable 反向索引(只失效引用了
	// 变量R.Score的表达式, 即when) → 提前return, 兜底子串匹配根本不执行
	// → AddScore 原子依然被缓存 → 仍只有1次真调用
	run(`E-B1: 变量真实存在 + Changed("R.Score")`, `
rule Loop "L" { when T.Amount > 0.0 && R.Score >= 0.0 then R.AddScore(1.0); Changed("R.Score"); }`)

	// E-B2: 同样的 when(变量存在) + Changed 传函数调用快照
	// 预测: 没有叫 "R.AddScore(1.0)" 的变量 → 走兜底子串 → 命中原子 → 4次
	run(`E-B2: 变量真实存在 + Changed("R.AddScore(1.0)")`, `
rule Loop "L" { when T.Amount > 0.0 && R.Score >= 0.0 then R.AddScore(1.0); Changed("R.AddScore(1.0)"); }`)

	// 引擎自述: 注入 Debug 级 zap 到 ast 包, 放出 Reset 的 Trace 日志,
	// 直接看 D4 场景里 Reset("R.Score") 实际重置了哪些快照
	fmt.Printf("\n════ 引擎自述: D4 场景 Reset(\"R.Score\") 触碰了谁 ════\n")
	cfg := zap.NewDevelopmentConfig()
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05")
	cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	cfg.DisableCaller = true
	cfg.DisableStacktrace = true
	zlog, _ := cfg.Build()
	ast.SetLogger(zlog) // 重新赋值 AstLog, 穿透 init 捕获
	run(`D4 重跑(带内部Trace)`, `
rule Loop "L" { when T.Amount > 0.0 then R.AddScore(1.0); Changed("R.Score"); }`)
}

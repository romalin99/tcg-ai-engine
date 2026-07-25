// 实验D续: 为什么第2轮起方法未被真正调用 —— 三种"解药"的对照验证
package main

import (
	"fmt"
	"log"

	"github.com/hyperjumptech/grule-rule-engine/ast"
	"github.com/hyperjumptech/grule-rule-engine/builder"
	"github.com/hyperjumptech/grule-rule-engine/engine"
	"github.com/hyperjumptech/grule-rule-engine/pkg"
)

type Txn struct{ Amount float64 }
type Res struct{ Score float64 }

func (r *Res) AddScore(s float64) {
	r.Score += s
	fmt.Printf("      [Go侧] AddScore 被真正调用! Score -> %.0f\n", r.Score)
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
	eng.Execute(dctx, kb) // 4轮后必然MaxCycle报错, 此处只关心Score
	fmt.Printf("  MaxCycle=4 跑满后 Score = %.0f\n", res.Score)
}

func main() {
	// 基线: 方法调用, 无任何失效 → 预测 Score=1 (首轮调用后被缓存)
	run("D1 基线: 纯方法调用", `
rule Loop "L" { when T.Amount > 0.0 then R.AddScore(1.0); }`)

	// 解药1: 赋值语句 → 预测 Score=4 (赋值走专用Execute路径, 不记忆化)
	run("D2 赋值语句", `
rule Loop "L" { when T.Amount > 0.0 then R.Score = R.Score + 1.0; }`)

	// 解药2: Changed 传【函数调用快照】→ 预测 Score=4
	// (Reset 的子串匹配兜底会命中该表达式原子, 每轮翻回未求值)
	run("D3 方法调用 + Changed(\"R.AddScore(1.0)\")", `
rule Loop "L" { when T.Amount > 0.0 then R.AddScore(1.0); Changed("R.AddScore(1.0)"); }`)

	// 反直觉负例: Changed 传【变量名 R.Score】→ 预测 Score 仍=1
	// (精确匹配到变量后走反向索引: 只失效"引用了变量R.Score"的表达式;
	//  R.AddScore(1.0) 这个原子里并不含变量 R.Score, 不会被失效)
	run("D4 方法调用 + Changed(\"R.Score\")", `
rule Loop "L" { when T.Amount > 0.0 then R.AddScore(1.0); Changed("R.Score"); }`)
}

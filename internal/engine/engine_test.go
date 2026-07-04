package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/romalin99/tcg-ai-engine/internal/model"
)

// 极简规则：金额达标就放行并打 9 折
const grlV1 = `
rule TestApprove "测试规则 v1" salience 10 {
    when
        Result.Decision == "" &&
        Order.Amount >= 100.0
    then
        Result.Decision = "approve";
        Result.Discount = 0.9;
        Result.AddHit("TestApprove");
}
`

// v2：同名规则改成 8 折，用于验证热更新后新逻辑生效
const grlV2 = `
rule TestApprove "测试规则 v2" salience 10 {
    when
        Result.Decision == "" &&
        Order.Amount >= 100.0
    then
        Result.Decision = "approve";
        Result.Discount = 0.8;
        Result.AddHit("TestApprove");
}
`

func newTestEngine(t *testing.T, grl string) *Engine {
	t.Helper()
	eng := New(zap.NewNop(), false)
	_, changed, err := eng.Reload([]model.Rule{{Name: "test.grl", Content: grl}}, "test")
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !changed {
		t.Fatal("首次 Reload 应当 changed=true")
	}
	return eng
}

func evaluate(t *testing.T, eng *Engine, amount float64) *model.Result {
	t.Helper()
	result := model.NewResult(0)
	err := eng.Evaluate(context.Background(), map[string]any{
		"Order":  &model.Order{Amount: amount},
		"Result": result,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return result
}

// 未加载规则时必须报 ErrNotReady，而不是空跑
func TestEvaluateBeforeReload(t *testing.T) {
	eng := New(zap.NewNop(), false)
	err := eng.Evaluate(context.Background(), map[string]any{"Result": model.NewResult(0)})
	if err != ErrNotReady {
		t.Fatalf("期望 ErrNotReady，得到 %v", err)
	}
}

// KnowledgeBase 实例经 sync.Pool 复用：连续执行结果必须一致
// （grule 在 Execute 开头会 Reset Retract 状态与工作内存，这里验证该前提）
func TestEvaluateReusesPooledInstance(t *testing.T) {
	eng := newTestEngine(t, grlV1)
	for i := range 10 {
		result := evaluate(t, eng, 200)
		if result.Decision != model.DecisionApprove || result.Discount != 0.9 {
			t.Fatalf("第 %d 次执行结果漂移: decision=%s discount=%v", i, result.Decision, result.Discount)
		}
	}
}

// 相同内容重复 Reload 不应重建（checksum 短路）
func TestReloadUnchanged(t *testing.T) {
	eng := newTestEngine(t, grlV1)
	info1, _ := eng.Info()
	info2, changed, err := eng.Reload([]model.Rule{{Name: "test.grl", Content: grlV1}}, "test")
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if changed {
		t.Fatal("内容未变，changed 应为 false")
	}
	if info2.LoadedAt != info1.LoadedAt {
		t.Fatal("内容未变不应产生新版本")
	}
}

// 坏规则 Reload 必须失败且不影响当前版本（热更新安全底线）
func TestReloadBadRuleKeepsOldVersion(t *testing.T) {
	eng := newTestEngine(t, grlV1)
	_, _, err := eng.Reload([]model.Rule{{Name: "bad.grl", Content: "rule Broken { when then }"}}, "test")
	if err == nil {
		t.Fatal("坏规则应当构建失败")
	}
	// 旧版本继续可用
	result := evaluate(t, eng, 200)
	if result.Discount != 0.9 {
		t.Fatalf("旧版本应继续生效, discount=%v", result.Discount)
	}
}

// 热更新后新逻辑立即生效
func TestReloadSwapsLogic(t *testing.T) {
	eng := newTestEngine(t, grlV1)
	if got := evaluate(t, eng, 200).Discount; got != 0.9 {
		t.Fatalf("v1 折扣应为 0.9, got %v", got)
	}
	_, changed, err := eng.Reload([]model.Rule{{Name: "test.grl", Content: grlV2}}, "test")
	if err != nil || !changed {
		t.Fatalf("热更新失败: changed=%v err=%v", changed, err)
	}
	if got := evaluate(t, eng, 200).Discount; got != 0.8 {
		t.Fatalf("v2 折扣应为 0.8, got %v", got)
	}
}

// 并发评估 + 并发热更新：用 -race 跑，验证 atomic 快照切换与 Pool 复用无数据竞争
func TestConcurrentEvaluateAndReload(t *testing.T) {
	eng := newTestEngine(t, grlV1)
	const goroutines = 16
	const iterations = 200

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines+1)
	for range goroutines {
		wg.Go(func() {
			for range iterations {
				result := model.NewResult(0)
				err := eng.Evaluate(context.Background(), map[string]any{
					"Order":  &model.Order{Amount: 200},
					"Result": result,
				})
				if err != nil {
					errCh <- err
					return
				}
				// 任意时刻要么 v1（0.9）要么 v2（0.8），不允许出现中间态
				if result.Discount != 0.9 && result.Discount != 0.8 {
					errCh <- fmt.Errorf("非法折扣值 %v", result.Discount)
					return
				}
			}
		})
	}
	// 评估进行中反复热更新 v1 ↔ v2
	wg.Go(func() {
		for i := range 50 {
			grl := grlV1
			if i%2 == 0 {
				grl = grlV2
			}
			if _, _, err := eng.Reload([]model.Rule{{Name: "test.grl", Content: grl}}, "test"); err != nil {
				errCh <- err
				return
			}
		}
	})
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

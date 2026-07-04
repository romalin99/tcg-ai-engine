package service

import (
	"context"
	"math"
	"slices"
	"testing"

	"go.uber.org/zap"

	"github.com/romalin99/tcg-ai-engine/internal/engine"
	"github.com/romalin99/tcg-ai-engine/internal/model"
	"github.com/romalin99/tcg-ai-engine/internal/repository"
	"github.com/romalin99/tcg-ai-engine/internal/types/req"
)

// newServiceWithRealRules 加载仓库根目录 rules/ 下的真实规则集，
// 这组测试同时充当规则行为的回归测试：改规则改坏了，这里会先红。
func newServiceWithRealRules(t *testing.T) *RiskService {
	t.Helper()
	repo := repository.NewFileRepository("../../rules")
	rules, err := repo.Load(context.Background())
	if err != nil {
		t.Fatalf("加载规则目录: %v", err)
	}
	eng := engine.New(zap.NewNop(), false)
	if _, _, err := eng.Reload(rules, repo.Source()); err != nil {
		t.Fatalf("构建规则库: %v", err)
	}
	return NewRiskService(eng, zap.NewNop())
}

// baseRequest 一笔各方面都正常的订单，各场景在此基础上做变异
func baseRequest() *req.EvaluateRequest {
	return &req.EvaluateRequest{
		Order: &model.Order{
			ID: "O-1", Amount: 12000, Quantity: 1, Freight: 20,
			PayMethod: "credit_card", Channel: "app", HourOfDay: 14,
			DeviceID: "dev-001", IPCountry: "CN",
		},
		Customer: &model.Customer{
			ID: "C-1", Name: "张三", Level: 5, Points: 8600,
			Tags:         []string{"loyal"},
			Address:      model.Address{Country: "CN", Province: "广东", City: "深圳"},
			RegisterDays: 800, TotalOrders: 40, RefundCount: 2,
		},
		Product: &model.Product{
			ID: "P-1", Name: "旗舰手机", Category: "electronics",
			Price: 12000, Stock: 100,
		},
		Merchant: &model.Merchant{
			ID: "M-1", Name: "旗舰数码店", Category: "electronics",
			Rating: 4.8, RefundRate: 0.02, YearsActive: 5,
		},
	}
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestApproveDiamondWithStackedDiscount(t *testing.T) {
	svc := newServiceWithRealRules(t)
	result, err := svc.Evaluate(context.Background(), baseRequest())
	if err != nil {
		t.Fatal(err)
	}

	if result.Decision != model.DecisionApprove {
		t.Fatalf("decision = %s, want approve（原因: %s）", result.Decision, result.RejectReason)
	}
	// 可信商户 -10 + 忠实老客 -10
	if result.RiskScore != -20 {
		t.Fatalf("riskScore = %v, want -20", result.RiskScore)
	}
	// 互斥组：钻石档独占（大额 97 折同样满足条件但被闸门挡住）
	if result.DiscountName != "diamond" {
		t.Fatalf("discountName = %q, want diamond（互斥组只允许一个档位生效）", result.DiscountName)
	}
	// 叠加规则在互斥档位之上生效：0.85 * 0.95
	if !almostEqual(result.Discount, 0.85*0.95) {
		t.Fatalf("discount = %v, want 0.8075（85 折再叠 95 折）", result.Discount)
	}
	// 满 2000 免运费（salience 高于钻石免运费，HitRules 里应是它）
	if result.Freight != 0 || !result.FreightFree {
		t.Fatalf("freight = %v freightFree=%v, want 0/true", result.Freight, result.FreightFree)
	}
	// VIP 2 倍积分
	if result.PointsEarned != 24000 {
		t.Fatalf("pointsEarned = %d, want 24000", result.PointsEarned)
	}
	if !almostEqual(result.FinalAmount, 9690) {
		t.Fatalf("finalAmount = %v, want 9690", result.FinalAmount)
	}
	for _, hit := range []string{
		"Relief_TrustedMerchant", "Relief_LoyalOldCustomer", "Decision_Approve",
		"Discount_Diamond", "Discount_LoyalTrustedExtra", "Freight_FreeBigOrder", "Points_VipRate",
	} {
		if !slices.Contains(result.HitRules, hit) {
			t.Fatalf("HitRules 缺少 %s，实际: %v", hit, result.HitRules)
		}
	}
}

func TestHardRejectBlacklistedCustomer(t *testing.T) {
	svc := newServiceWithRealRules(t)
	request := baseRequest()
	request.Customer.Blacklisted = true

	result, err := svc.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsRejected() || result.RejectReason != "客户在黑名单" {
		t.Fatalf("want 拒单/客户在黑名单, got %s/%s", result.Decision, result.RejectReason)
	}
	if result.RiskScore != 100 {
		t.Fatalf("riskScore = %v, want 100", result.RiskScore)
	}
	// Complete() 终止推理：后面的评分/折扣/运费/积分规则一条都不该执行
	if len(result.HitRules) != 1 || result.HitRules[0] != "HardReject_CustomerBlacklist" {
		t.Fatalf("Complete() 后不应有其他规则执行, HitRules = %v", result.HitRules)
	}
	if result.Discount != 1.0 || result.DiscountName != "" {
		t.Fatalf("拒单订单不应有折扣: %v %q", result.Discount, result.DiscountName)
	}
	if result.FinalAmount != 0 || result.PointsEarned != 0 {
		t.Fatalf("拒单订单应付金额/积分应为 0: %v %d", result.FinalAmount, result.PointsEarned)
	}
}

func TestHardRejectRestrictedAbroad(t *testing.T) {
	svc := newServiceWithRealRules(t)
	request := baseRequest()
	request.Product.Restricted = true
	request.Customer.Address.Country = "JP"

	result, err := svc.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsRejected() || result.RejectReason != "管制商品不支持海外收货地址" {
		t.Fatalf("want 拒单/管制商品不支持海外收货地址, got %s/%s", result.Decision, result.RejectReason)
	}
}

// 评分累加到 [35, 60) 区间 → 转人工审核
func TestReviewByAccumulatedScore(t *testing.T) {
	svc := newServiceWithRealRules(t)
	request := &req.EvaluateRequest{
		// 凌晨下单 +10，缺设备指纹 +15
		Order: &model.Order{
			ID: "O-2", Amount: 800, Quantity: 1, Freight: 12,
			PayMethod: "credit_card", HourOfDay: 2, DeviceID: "", IPCountry: "",
		},
		// 海外地址 +10；TotalOrders < 5 不触发退款率规则
		Customer: &model.Customer{
			ID: "C-2", Level: 1,
			Address:      model.Address{Country: "US", City: "LA"},
			RegisterDays: 200, TotalOrders: 3, RefundCount: 1,
		},
		Product:  &model.Product{ID: "P-2", Category: "books", Stock: 10},
		Merchant: &model.Merchant{ID: "M-2", Rating: 4.0, RefundRate: 0.05, YearsActive: 2},
	}

	result, err := svc.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RiskScore != 35 {
		t.Fatalf("riskScore = %v, want 35（10+10+15）", result.RiskScore)
	}
	if result.Decision != model.DecisionReview {
		t.Fatalf("decision = %s, want review", result.Decision)
	}
	// review 订单：无折扣命中、正常收运费、不给积分
	if result.DiscountName != "" || !almostEqual(result.Discount, 1.0) {
		t.Fatalf("不应命中折扣: %q %v", result.DiscountName, result.Discount)
	}
	if result.Freight != 12 {
		t.Fatalf("freight = %v, want 12", result.Freight)
	}
	if result.PointsEarned != 0 {
		t.Fatalf("review 订单不应有积分, got %d", result.PointsEarned)
	}
	if !almostEqual(result.FinalAmount, 812) {
		t.Fatalf("finalAmount = %v, want 812", result.FinalAmount)
	}
}

// 评分累加 ≥ 60 → 决策规则拒单（非 Complete 路径的互斥闸门联动）
func TestRejectByAccumulatedScore(t *testing.T) {
	svc := newServiceWithRealRules(t)
	request := &req.EvaluateRequest{
		// 凌晨 +10，缺设备 +15，虚拟商品大额 +25
		Order: &model.Order{
			ID: "O-3", Amount: 6000, Quantity: 1, Freight: 10,
			PayMethod: "credit_card", HourOfDay: 2, DeviceID: "",
		},
		Customer: &model.Customer{
			ID: "C-3", Level: 2,
			Address:      model.Address{Country: "CN", Province: "浙江"},
			RegisterDays: 100, TotalOrders: 10, RefundCount: 1,
		},
		Product: &model.Product{ID: "P-3", Category: "software", Stock: 50, IsVirtual: true},
		// 退款率 0.15 +20（未超 0.20，不构成高风险商户硬拒单）
		Merchant: &model.Merchant{ID: "M-3", Rating: 4.0, RefundRate: 0.15, YearsActive: 3},
	}

	result, err := svc.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RiskScore != 70 {
		t.Fatalf("riskScore = %v, want 70（10+15+25+20）", result.RiskScore)
	}
	if !result.IsRejected() || result.RejectReason != "风险分过高" {
		t.Fatalf("want 拒单/风险分过高, got %s/%s", result.Decision, result.RejectReason)
	}
	// 拒单后闸门生效：折扣/免运费/积分全部避让
	if result.DiscountName != "" || result.FreightFree || result.PointsRate != 0 {
		t.Fatalf("拒单后不应命中营销规则: %q %v %v",
			result.DiscountName, result.FreightFree, result.PointsRate)
	}
}

// 同档位互斥：金卡大单（0.86）优先于金卡（0.88），且只能命中一个
func TestDiscountMutualExclusionGoldTier(t *testing.T) {
	svc := newServiceWithRealRules(t)
	request := baseRequest()
	request.Customer.Level = 4
	request.Customer.Tags = nil // 去掉 loyal，隔离叠加规则
	request.Order.Amount = 15000

	result, err := svc.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.DiscountName != "gold_big" {
		t.Fatalf("discountName = %q, want gold_big", result.DiscountName)
	}
	if !almostEqual(result.Discount, 0.86) {
		t.Fatalf("discount = %v, want 0.86（gold 0.88 应被互斥闸门挡住）", result.Discount)
	}
	if !almostEqual(result.FinalAmount, 15000*0.86) {
		t.Fatalf("finalAmount = %v, want %v", result.FinalAmount, 15000*0.86)
	}
}

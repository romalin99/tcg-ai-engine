#!/usr/bin/env bash
# 演示脚本：起服务后依次体验 评估 / 查看规则 / 热更新。
# 用法：先 make run（或 go run ./cmd/api），另开终端执行 ./scripts/demo.sh
set -euo pipefail

BASE="${BASE:-http://localhost:18080}"

say() { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }

say "健康检查"
curl -s "$BASE/healthz"; echo

say "当前生效的规则集（来源/指纹/规则清单）"
curl -s "$BASE/api/v1/rules" | python3 -m json.tool

say "场景一：钻石会员大单 → approve + 互斥折扣(diamond) + 叠加折扣 + 免运费 + 双倍积分"
curl -s -X POST "$BASE/api/v1/risk/evaluate" -H 'Content-Type: application/json' -d '{
  "order":    {"id":"O-1001","amount":12000,"quantity":1,"freight":20,"pay_method":"credit_card","channel":"app","hour_of_day":14,"device_id":"dev-001","ip_country":"CN"},
  "customer": {"id":"C-1001","name":"张三","level":5,"points":8600,"tags":["loyal"],"address":{"country":"CN","province":"广东","city":"深圳"},"register_days":800,"total_orders":40,"refund_count":2,"chargeback_count":0},
  "product":  {"id":"P-1","name":"旗舰手机","category":"electronics","price":12000,"stock":100},
  "merchant": {"id":"M-1","name":"旗舰数码店","category":"electronics","rating":4.8,"refund_rate":0.02,"years_active":5}
}' | python3 -m json.tool

say "场景二：黑名单客户 → 硬拒单（Complete() 终止推理）"
curl -s -X POST "$BASE/api/v1/risk/evaluate" -H 'Content-Type: application/json' -d '{
  "order":    {"id":"O-1002","amount":500,"quantity":1,"freight":10,"pay_method":"credit_card","hour_of_day":10,"device_id":"dev-002","ip_country":"CN"},
  "customer": {"id":"C-1002","name":"李四","level":2,"blacklisted":true,"address":{"country":"CN","province":"北京","city":"北京"},"register_days":100},
  "product":  {"id":"P-2","name":"图书","category":"books","price":500,"stock":10},
  "merchant": {"id":"M-2","name":"书店","category":"books","rating":4.2,"refund_rate":0.03,"years_active":2}
}' | python3 -m json.tool

say "场景三：凌晨+无设备指纹+海外地址 → 风险分 35 → 转人工审核"
curl -s -X POST "$BASE/api/v1/risk/evaluate" -H 'Content-Type: application/json' -d '{
  "order":    {"id":"O-1003","amount":800,"quantity":1,"freight":12,"pay_method":"credit_card","hour_of_day":2,"device_id":"","ip_country":""},
  "customer": {"id":"C-1003","name":"Tom","level":1,"address":{"country":"US","city":"LA"},"register_days":200,"total_orders":3,"refund_count":1},
  "product":  {"id":"P-3","name":"图书","category":"books","price":800,"stock":10},
  "merchant": {"id":"M-3","name":"普通店铺","category":"books","rating":4.0,"refund_rate":0.05,"years_active":2}
}' | python3 -m json.tool

say "手动触发热更新（内容没变时 changed=false，不重建）"
curl -s -X POST "$BASE/api/v1/rules/reload" | python3 -m json.tool

cat <<'EOF'

热更新演示：
  1. 改一条规则，比如把钻石折扣 0.85 改成 0.80：
       sed -i '' 's/Result.Discount = 0.85;/Result.Discount = 0.80;/' rules/040_discount.grl
  2. 等 5 秒（reload_interval_seconds），观察服务日志出现「规则热更新生效」
  3. 重发场景一请求，discount 立即变化 —— 全程无需重启服务
EOF

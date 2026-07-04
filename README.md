# tcg-ai-engine

基于 [grule-rule-engine](https://github.com/hyperjumptech/grule-rule-engine) 的电商风控规则引擎服务。

- 4 个输入 Fact（Order / Customer / Product / Merchant）+ 1 个输出 Fact（Result，黑板模式）
- 40 条规则，salience 分层 + 三种互斥手法 + 大量跨 Fact 组合条件
- 规则动态加载：默认 `rules/` 目录 `*.grl`，可切 Oracle 规则表
- 规则热更新：轮询指纹比对 + 原子切换，改规则无需重启服务
- `RuleEngine` 封装：`atomic.Pointer` 快照 + `sync.Pool` 复用 KnowledgeBase，并发安全（`-race` 验证）

## 快速开始

```bash
make run          # 起服务（文件规则源，:8080）
make demo         # 另开终端：跑通 评估 / 查规则 / 热更新
make test         # 单测（-race，含基于真实规则集的场景回归）
make tutorial     # 运行 grule 入门教学示例（examples/tutorial）
```

## 目录结构（参考 tcg-ucs-fe）

```
├── cmd/
│   ├── api/                # 服务入口：配置 → 日志 → 数据源 → 首次加载(fail-fast) → 轮询 → HTTP → 优雅停机
│   └── ruleloader/         # 把 rules/*.grl 批量 MERGE 进 Oracle 规则表的工具
├── config/                 # config.toml（默认）/ config.oracle.toml / dev|sit|prod.toml（环境配置）
├── rules/                  # 40 条 GRL 规则，按 salience 分层拆文件
├── internal/
│   ├── config/             # TOML 配置加载与校验
│   ├── model/              # Fact 定义：Order/Customer/Product/Merchant + Result + Rule
│   ├── engine/             # ★ RuleEngine 封装：并发复用 + 热更新 + 推理跟踪监听器
│   ├── repository/         # 规则数据源抽象：FileRepository / OracleRepository
│   ├── service/            # 业务编排：组装 Fact → 跑规则 → Go 侧结算
│   ├── handler/ router/ middleware/  # HTTP 层（gofiber/fiber v3）
│   └── types/req resp      # 请求/响应体
├── pkg/                    # 平台组件（自 tcg-ucs-fe 引入：kafka/redis/consul/telemetry/metrics/gos/...）
│   ├── logs/               # zap 日志（文件轮转 + 行为日志；New() 保留简单控制台入口）
│   └── oracle/             # Oracle 连接封装（go-ora 的 Open + godror 连接池 Config 两套并存）
├── scripts/
│   ├── sql/rules_table.sql # Oracle 规则表 DDL
│   └── demo.sh             # curl 演示脚本
└── examples/tutorial/      # grule 入门教学示例（原 1main.go，含逐行注释）
```

## 规则设计

### salience 分层（优先级）

冲突消解规则：每个 cycle 只执行候选集中 salience 最高的一条。分层保证"先定生死，再谈优惠"。

| salience | 文件 | 职责 | 条数 |
|---|---|---|---|
| 1000~994 | 010_risk_reject.grl | 硬拒单，命中即 `Complete()` 停机 | 7 |
| 890~871 | 020_risk_score.grl | 风险分累加/减免，`Retract` 防重复计分 | 15 |
| 500~498 | 030_risk_decision.grl | 按累计分定档：reject / review / approve | 3 |
| 300~289 | 040_discount.grl | 折扣档位（互斥组）+ 叠加折扣 | 8 |
| 200~196 | 050_freight.grl | 免运费 / 偏远地区附加费 | 4 |
| 152~150 | 060_points.grl | 积分倍率（互斥组，仅 approve） | 3 |

### 三种互斥手法

1. **`Complete()` 停机**（010）：黑名单等硬拒单命中后整个推理终止，是最强互斥；
2. **闸门字段**（030/040/060）：`Result.Decision == ""`、`Result.DiscountName == ""`、
   `Result.PointsRate == 0.0` 作为前置条件，salience 高者先写入、闸门关闭，其余永不触发
   ——一单只有一个决策、一个折扣档位、一个积分倍率；
3. **`Retract` 一次性规则**（020）：评分规则互不互斥、可同时命中多条，但各自执行一次后退场，
   防止"改分 → 重新评估 → 再次命中"的死循环。

叠加规则（`Discount_LoyalTrustedExtra`）反向演示：不进互斥组、但要求已有档位，
在互斥结果之上折上折——互斥与叠加只是闸门条件写法的差别。

### grule 表达式缓存注意事项

被 when 引用的 Result 字段（Decision/RiskScore/DiscountName/...）在 then 里必须用 `=`
直接赋值，grule 才会失效对应缓存并重新求值；只写不读的字段（HitRules）才能用方法
（`AddHit`）修改。输入 Fact 在推理期间只读，方法调用（`Customer.IsNew()`）是安全的。

## RuleEngine 并发模型（internal/engine）

```
        ┌────────────────────── Engine ──────────────────────┐
        │ grule.GruleEngine（无状态，全局一个）                  │
        │ atomic.Pointer[snapshot] ──► snapshot v2 (当前版本)   │
        │                              ├ KnowledgeLibrary     │
        │                              ├ Info(来源/指纹/清单)   │
        │                              └ sync.Pool ── KB 实例  │
        └─────────────────────────────────────────────────────┘
```

- **KnowledgeBase 实例**持有执行期状态（Retract 标记、表达式缓存），不能被两个 goroutine
  同时使用；但 grule 在每次 `Execute` 开头会重置这些状态，因此实例可以放进 `sync.Pool`
  复用，省掉每次请求克隆 AST 的开销；
- **热更新**：轮询数据源 → 全量内容 SHA-256 指纹比对 → 变了才重建 KnowledgeLibrary →
  `atomic.Pointer` 整体换掉 snapshot。进行中的请求继续用旧快照跑完，新请求拿新快照，
  旧版本无人引用后被 GC——全程无锁、无需停服；
- **安全底线**：新规则构建失败（语法错误、规则重名）只告警不切换，当前版本继续服务；
- 手动 `POST /api/v1/rules/reload` 与后台轮询共用同一条 `ReloadOnce` 链路。

## 规则动态加载与热更新

```toml
[rules]
source = "file"             # file / oracle
reload_interval_seconds = 5 # <=0 关闭自动热更新
```

**文件源（默认）**：加载 `rules/` 下全部 `*.grl`（按文件名排序保证指纹稳定）。
改规则 → 保存 → 下个轮询周期自动生效：

```bash
sed -i '' 's/Result.Discount = 0.85;/Result.Discount = 0.80;/' rules/040_discount.grl
# 5 秒内服务日志出现「规则热更新生效」，重发请求 discount 即变化
```

**Oracle 源**：

```bash
# 1. 建表
sqlplus user/pass@host:1521/svc @scripts/sql/rules_table.sql
# 2. 导入规则（MERGE，可反复执行）
go run ./cmd/ruleloader -dsn 'oracle://user:pass@host:1521/svc' -dir rules
# 3. 用 Oracle 配置起服务
go run ./cmd/api -f config/config.oracle.toml
```

之后直接 `UPDATE RISK_RULES SET GRL_CONTENT = ...`（或 `ENABLED = 0` 下线规则），
服务在下个轮询周期自动热加载。驱动用纯 Go 的 go-ora，本地无需 Oracle Instant Client；
如需与 tcg-ucs-fe 生产一致换 godror，只改 `pkg/oracle` 一处。

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/risk/evaluate | 风控评估（body：order/customer/product/merchant 四个 Fact） |
| GET  | /api/v1/rules | 当前生效规则集：来源、指纹、规则清单、生效时间 |
| POST | /api/v1/rules/reload | 手动触发热更新（内容没变时 changed=false） |
| GET  | /healthz | 健康检查 |
| GET  | /livez、/readyz | 存活 / 就绪探针（fiber healthcheck） |
| GET  | /metrics、/monitor | Prometheus 指标 / 实时监控面板 |
| GET  | /swagger/ | Swagger UI（`make swagger` 重新生成 docs） |

评估响应示例（场景：钻石会员大单）：

```json
{
  "code": 0, "message": "ok",
  "data": {
    "order_id": "O-1001",
    "result": {
      "risk_score": -20, "decision": "approve",
      "discount": 0.8075, "discount_name": "diamond", "extra_discount": true,
      "freight": 0, "freight_free": true,
      "points_rate": 2, "points_earned": 24000,
      "final_amount": 9690,
      "hit_rules": ["Relief_TrustedMerchant", "Relief_LoyalOldCustomer",
                    "Decision_Approve", "Discount_Diamond", "Discount_LoyalTrustedExtra",
                    "Freight_FreeBigOrder", "Points_VipRate"]
    },
    "engine": {"checksum": "…", "rule_count": 40}
  }
}
```

## 测试

- `internal/engine`：未加载报错、Pool 复用一致性、指纹短路、坏规则不切换、热更新换逻辑、
  16 goroutine 并发评估 × 并发热更新（`-race`）；
- `internal/service`：基于**真实 rules/ 目录**的 6 个场景回归（钻石大单、黑名单硬拒、
  管制品海外、评分转审、评分拒单、金卡档位互斥）——改规则改坏了这里先红；
- `internal/repository`：文件加载排序与空目录报错。

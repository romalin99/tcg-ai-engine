# tcg-ai-engine

基于 [grule-rule-engine](https://github.com/hyperjumptech/grule-rule-engine) 的电商风控规则引擎服务。

- 4 个输入 Fact（Order / Customer / Product / Merchant）+ 1 个输出 Fact（Result，黑板模式）
- 40 条规则，salience 分层 + 三种互斥手法 + 大量跨 Fact 组合条件
- 规则动态加载：默认 `rules/` 目录 `*.grl`，可切 Oracle 规则表（godror 连接池）
- 规则热更新：轮询指纹比对 + 原子切换，改规则无需重启服务
- `RuleEngine` 封装：`atomic.Pointer` 快照 + `sync.Pool` 复用 KnowledgeBase，并发安全（`-race` 验证）
- HTTP 层 gofiber/fiber v3 + sonic JSON；内置 Prometheus 指标、Swagger UI、pprof、
  OTel 链路追踪与行为日志等平台能力
- 进阶特性示例：链式调用（`Fact.Func().Field`）、数组/map/嵌套结构访问（含越界防护实践）、
  JSON 直接定义 Fact（见 `examples/advanced`）

## 快速开始

```bash
ENV=dev make run  # 起服务（文件规则源，dev 监听 :18080；ENV 不设默认 prod）
make demo         # 另开终端：跑通 评估 / 查规则 / 热更新
make test         # 单测（-race，含基于真实规则集的场景回归）
make tutorial     # 运行 grule 入门教学示例（examples/tutorial）
make              # 查看全部构建/质量/文档目标（help）

go run ./examples/advanced  # 进阶示例：链式调用 / 数组map嵌套访问 / JSON Fact
```

## 启动框架

`cmd/api` 的初始化链：

```
viper 配置（ENV 选择 config/{dev,sit,prod}.toml，-f 可覆盖）
→ logs 单例（*zap.Logger 桥接给 internal/* 与 grule 的 ast.SetLogger）
→ metrics / telemetry（可开关）
→ 规则数据源（file / oracle）→ 首次全量加载（fail-fast）→ 热更新轮询
→ 可选组件：kafka producer（配置了 brokers 才创建）/ pprof / memstats
→ fiber（sonic JSON + cors / recover / otel / 行为日志 / 访问日志）
→ 路由（业务 + prometheus + swagger，组级限流 800/s + 路由级超时）
→ 信号驱动分级优雅停机（HTTP → pprof → 轮询 → producer → 连接 → 日志落盘）
```

## 配置

`ENV=dev|sit|prod` 选择 `config/{env}.toml`（默认 **prod**）；`-f <file>` 直接指定文件。
主要配置段：

| 段 | 说明 |
|---|---|
| 顶层 | `name/host/port/timeout/bodyLimit/shutdownTimeout`（dev 端口 18080） |
| `[rules]` | 规则数据源：`source=file/oracle`、`reload_interval_seconds`（<=0 关闭热更新）、`grl_trace` |
| `[rules.file]` / `[rules.oracle]` | 文件源目录 / Oracle 规则表名（默认 RISK_RULES） |
| `[oracle]` | godror 连接池：`user/passwd/addr_connect_stringer` + 池参数（规则 Oracle 源复用此池） |
| `[log]` | 日志：`mode=console/file`、级别、轮转、行为日志目录（dev 走 console） |
| `[kafka.producer/consumer]` | franz-go 配置；dev 的 brokers 置空默认不连 |
| `[telemetry]` / `[pprof]` | OTel 上报（endpoint/采样率/skip_paths）/ pprof 独立端口（dev :6068） |
| `[timeouts]` | 路由级超时档位（quick/normal/long/upload，秒） |
| `[redis]` / `[consul]` / `[bigCache]` | 平台组件配置（预留，尚未在启动链接线） |

## 目录结构

```
├── cmd/
│   ├── api/                # 服务入口：application 容器 + 依赖序初始化 + 分级优雅停机
│   └── ruleloader/         # 把 rules/*.grl 批量 MERGE 进 Oracle 规则表的工具（godror）
├── config/                 # viper 环境配置：ENV 选择 dev/sit/prod.toml
├── rules/                  # 40 条 GRL 规则，按 salience 分层拆文件
├── docs/                   # swag 生成的 OpenAPI 文档（make swagger 重新生成）
├── internal/
│   ├── config/             # viper 配置：聚合 pkg 子配置 + [rules] 数据源段
│   ├── model/              # Fact 定义：Order/Customer/Product/Merchant + Result + Rule
│   ├── engine/             # ★ RuleEngine 封装：并发复用 + 热更新 + 推理跟踪监听器
│   ├── repository/         # 规则数据源抽象：FileRepository / OracleRepository
│   ├── service/            # 业务编排：组装 Fact → 跑规则 → Go 侧结算
│   ├── handler/ router/ middleware/  # HTTP 层（fiber v3：路由/限流/超时/指标/错误壳/日志）
│   └── types/req resp      # 请求/响应体（统一 JSON 壳：code/message/data）
├── pkg/                    # 平台组件：kafka/redis/consul/telemetry/metrics/gos/...
│   ├── logs/               # zap 日志（文件轮转 + 行为日志；New() 保留简单控制台入口）
│   └── oracle/             # Oracle 连接封装（godror 连接池 + 状态监控/metrics）
├── scripts/
│   ├── sql/rules_table.sql # Oracle 规则表 DDL
│   └── demo.sh             # curl 演示脚本
└── examples/
    ├── tutorial/           # grule 入门教学示例（含逐行注释）
    └── advanced/           # 进阶示例：链式调用、数组/map/嵌套访问（越界防护）、JSON Fact
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
go run ./cmd/ruleloader -user user -password pass -connect host:1521/svc -dir rules
# 3. 改环境配置切 Oracle 源后起服务：
#    config/{env}.toml 里 [rules] source = "oracle"，并在 [oracle] 段配置 user/passwd/addr_connect_stringer
ENV=dev make run
```

之后直接 `UPDATE RISK_RULES SET GRL_CONTENT = ...`（或 `ENABLED = 0` 下线规则），
服务在下个轮询周期自动热加载。驱动用 godror（CGO/ODPI-C，与生产一致），运行环境需要
Oracle Client 库；连接参数配置在 config/{env}.toml 的 `[oracle]` 段，规则源与业务共用一个连接池。

## 进阶特性示例（examples/advanced）

`go run ./examples/advanced` 演示三个 grule 进阶用法：

- **链式调用**：`Customer.LastOrder().Amount`、`Customer.Membership().Level` ——
  方法返回对象后继续取字段/再调方法；
- **数组 / map / 嵌套结构访问**：
  `Fact.SubFacts[1].SubFacts[2].AnIntArray[12] > 100 && Fact.SubMaps["Key"].AnIntArray[0] == 1000`。
  ⚠️ 裸下标越界会在求值时 panic；当前版本引擎会 recover 成该条规则的评估错误并**静默跳过**
  （进程不崩、`Execute` 不报错，仅日志可见）——规则悄悄失效比崩溃更隐蔽，生产环境必须在
  Fact 层做边界保护（示例中的 `SubInt`/`MapInt` 安全访问器模式）。另外 GRL 整型字面量按
  int64 传参，Fact 方法参数要声明成 `int64`；
- **JSON 定义 Fact**：`dataContext.AddJSON("J", jsonBytes)` 后规则里直接
  `J.user.tags[0]`、`J.user.stats.orders`，无需预定义 Go struct，适合规则和数据结构
  都要动态下发的场景。

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/v1/risk/evaluate | 风控评估（body：order/customer/product/merchant 四个 Fact） |
| GET  | /api/v1/rules | 当前生效规则集：来源、指纹、规则清单、生效时间 |
| POST | /api/v1/rules/reload | 手动触发热更新（内容没变时 changed=false） |
| GET  | /healthz、/ping | 健康检查 |
| GET  | /livez、/readyz | 存活 / 就绪探针（fiber healthcheck） |
| GET  | /metrics、/monitor | Prometheus 指标 / 实时监控面板 |
| GET  | /swagger/ | Swagger UI（`make swagger` 重新生成 docs） |

统一响应壳 `{code, message, data}`：`code=0` 成功；业务错误码 4xxxx/5xxxx（前三位对应
HTTP 状态码），框架层错误（404/405/408 等）同样返回该壳，`code = HTTP 状态码 ×100`。

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
- `internal/repository`：文件加载排序与空目录报错；
- `pkg/helper`、`pkg/math`：平台工具函数单测。

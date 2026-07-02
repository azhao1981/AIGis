# EE ROADMAP — 企业版（Enterprise Edition）路线图

> 本文件是 **EE（`ee/`）专属**路线图与变更记录，独立于开源核心的 [`TODO.md`](TODO.md) / [`DONE.md`](DONE.md)。
> EE 采用 open-core：AGPLv3 开源核心 + 专有 `ee/`；依赖方向严格单向 **`ee/ → internal/`**，核心永不 import ee。
> 落地方式全部走既有 seam：`AuthProvider` 接口 / `server.Middleware`（`Use`）/ `SetUsageSink` / `SetQuotaLimiter`。

---

## 已完成（DONE）

### P0 — 多租户地基
- **租户上下文**：`AIGisContext` 加 `Tenant`/`Subject`；核心定义 context-key 与 `WithTenant`/`TenantFromContext`（核心读、ee 写），日志与审计（`audit.Record.Tenant`）按租户打点
- **用量落库**：核心 `usage.Sink` seam（默认 `NopSink`）；`ee/billing`
  - 内存版 `MeteringSink`（按租户聚合 + 日志）
  - `PostgresSink`：pgxpool + 异步批量写；只用标准 `INSERT`，不感知目标表是否 hypertable（可移植）
  - `migrations/001_usage_events.sql`：标准 DDL + 2 索引 + **唯一一行** `create_hypertable`（Timescale 隔离，去掉即为普通 PG 表）
- **按租户配额**：核心 `quota.Limiter` seam（默认 `AllowAll`）；`ee/quota` `ConcurrencyLimiter`（per-tenant 并发上限，超限由核心返 429）

### P1 — 生产化
- **① DSN 环境变量**：`AIGIS_EE_BILLING_DSN` 优先于 `ee.billing.dsn`，数据库密码不落 `config.yaml`
- **② 用量查询 API**：`GET /admin/usage?tenant=&from=&to=&granularity=hour|day|month`，标准 `date_trunc` 聚合（非 Timescale `time_bucket`，保持可移植）
- **③ API key/租户落库**：`ee/auth` `PostgresAPIKeyProvider`
  - **仅存 SHA-256 哈希**（明文 key 永不落库）；内存快照 + `Reload`（请求路径不打库）
  - `/admin/keys` 增（POST）删（DELETE 软禁用）查（GET）；启动从 `ee.auth.api_keys` bootstrap 种子 key（幂等 upsert）
  - `migrations/001_api_keys.sql`
- **④ 配额跨副本**：`ee/quota` `RedisLimiter`（Lua 原子 check-and-incr，TTL 自愈防泄漏 slot，Redis 挂了 **fail-open**）；`ee.quota.redis_addr` 有则分布式、否则内存单副本；与内存版共用同一 `Limiter` seam

### Hardening — 加固
- **测试补齐**：`ee/auth`（`hashKey`）、`ee/quota`（`ConcurrencyLimiter` per-tenant/default/unlimited/幂等释放）、核心 `applyUsageTokens`（OpenAI/Anthropic/total 兜底）单测
- **`/admin/*` 权限分级（RBAC）**：`Principal.Admin` + `auth.IsAdmin`；`api_keys.is_admin` 列（`migrations/002_api_keys_admin.sql`）；`/admin/keys`、`/admin/usage` 非 admin 返 **403**、无 key 返 401；bootstrap 由 `ee.auth.admin_keys` 列表标记管理员 key
- **用量落库可靠性**：`PostgresSink` 写库失败改为**退避重试**（默认 3 次，200ms→400ms→800ms），耗尽才丢；丢弃不再静默——队列满 `dropped_full` / 重试耗尽 `dropped_write` 计数（`Stats()` + `Close` 汇总日志）；参数经 `SinkOptions` 可配（`ee.billing.{queue_size,batch_size,flush_interval_ms,max_retries}`，缺省即原值，零行为变更）；`NewPostgresSink` 签名不变（内部转 `NewPostgresSinkWithOptions`），`writeFn` seam 支持无库单测

---

## 待办（TODO / 观察项，按需）

- [ ] **用量不丢的强保证（WAL）**：当前突发过载/DB 长挂仍会丢弃（有计数、不静默）；若计费要求「一条不丢」，需落盘缓冲（WAL / 磁盘队列）重放——重量级，按需再上
- [ ] **配额维度扩展**：现仅并发数，可加 QPS / token 配额（token 配额需读用量库）
- [ ] **Admin 接口完善**：分页 / 按租户过滤 keys；用量导出（CSV）；审计谁改了 key
- [ ] **多实例配置一致性**：API key 快照靠重启或显式 `Reload`，多副本下需要广播刷新（如 Redis pub/sub 或轮询 TTL）

## 相关 OSS 待澄清项（见 TODO.md B 段，按需）
- Azure OpenAI legacy（`?api-version=` + `api-key` 头）
- Anthropic 原生 `/v1/messages` / Bedrock SigV4

---

## 配置速查（EE 专属 key）

```yaml
ee:
  auth:
    api_keys:            # apiKey -> tenant（bootstrap 种子 / 无 DB 时的静态源）
      key-xxx: "tenant-a"
    admin_keys:          # 其中拥有 /admin/* 权限的 key 列表
      - "key-xxx"
  billing:
    dsn: "postgres://..."   # 亦可用环境变量 AIGIS_EE_BILLING_DSN（优先，密码不落盘）
    queue_size: 4096        # 异步写队列容量（0=默认 4096）
    batch_size: 100         # 每次入库批量条数（0=默认 100）
    flush_interval_ms: 2000 # 定时刷盘间隔毫秒（0=默认 2000）
    max_retries: 3          # 批量写失败退避重试次数（0=默认 3；负数=不重试）
  quota:
    default: 0              # 每租户并发上限，0=不限
    per_tenant:            # 单租户覆盖
      tenant-a: 10
    redis_addr: ""         # 设置后配额跨副本共享（分布式）；否则单进程内存
    redis_password: ""
    redis_db: 0
```

> `ee.billing.dsn`（或 `AIGIS_EE_BILLING_DSN`）同时作为 auth 注册表与用量库的**共享 DSN**：设置后 `/admin/keys` + `/admin/usage` 启用、API key 走 DB；未设置则退回内存 + 静态 config key。

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
- **多副本 API key 一致性**：`PostgresAPIKeyProvider.StartRefresh(interval)` 后台按 `ee.auth.reload_interval_sec`（默认 30s，≤0 禁用）定时 `Reload`，另一副本上 `/admin/keys` 创建/吊销的 key 在一个周期内自动生效——本副本 `CreateKey`/`RevokeKey` 仍立即 Reload；`Close` 停 ticker（`done`+`wg`+`sync.Once`），`refreshFn` seam 支持无库单测（`-race`）；真机双副本 e2e：A 创建/吊销→B 刷新后跟随，全程 B 不重启
- **API key 变更审计**：`ee/auth` `api_key_audit` 表（`migrations/003_api_key_audit.sql`）记录谁在何时创建/吊销了哪个 key——`CreateKey`/`RevokeKey` 增 `Actor` 参数，成功后写一行审计（**仅存 key_hash，绝不落明文**；actor 取自请求 Principal，bootstrap 用 `subject="bootstrap"`）；写审计失败**不回滚**主操作（key 已改成功），`log.Error` 响亮留痕（fail-loud）；`GET /admin/keys/audit?key=&action=&limit=`（admin only，`?key=` 明文入参先哈希再比对），`ListAudit` 按 `ts DESC`；真机 e2e：create/revoke 各留痕、actor 正确、按 key/action 过滤精确
- **Admin 接口完善**：用量 CSV 导出 `GET /admin/usage?...&format=csv`（`text/csv` 附件，复用 `QueryUsage`，表头 + 每桶一行）；keys 按租户过滤 + 分页 `GET /admin/keys?tenant=&limit=&offset=`（`ListKeys(KeyQuery)`，`WHERE ($1='' OR tenant=$1) LIMIT/OFFSET`，无参=原全量行为，零破坏）；审计同加 `offset` 分页；真机 e2e：CSV 头/行正确、tenant 过滤命中、limit/offset 翻页精确
- **管理 UI（能力发现分层）**：核心 `internal/adminui` 内嵌单页仪表盘（`go:embed index.html`，零 npm/构建链）+ 能力发现端点——`GET /ui` 出页面、`GET /ui/capabilities` 出 `{"panels":[...]}`；OSS 只报 `["status"]`（`/health`+`/metrics` 只读状态面板，无需 token），页面按 capabilities 动态渲染 Tab，同一页适配两种构建**不 fork**；EE `ee/adminui` `CapabilitiesMiddleware`（`server.Middleware` 拦 `/ui/capabilities`）覆写为四面板 `status/keys/usage/audit`，DSN 有时 `serve.go` `srv.Use` 点亮，数据 Tab 直连既有 `/admin/*`（token 存 localStorage，请求带 `Authorization: Bearer`）；`/ui`+`/ui/capabilities` 进 auth `skipPaths`（首屏无 token 可加载，`/admin/*` 数据仍 401 守护）；依赖方向 CORE_CLEAN（`internal/adminui` 不引 ee/pgx/redis）；真机 e2e：OSS 只 status、EE 点亮四 Tab、无 token /ui 返 200 而 /admin/keys 返 401

### SaaS — 用户登录地基（batch A）
- **用户/密码登录（人 vs 机并行，非替换）**：`ee/auth` 加 `users` 表（`migrations/004_users.sql`，email 唯一 + tenant + is_admin + enabled）、`UserStore`（**bcrypt 哈希，明文永不落库**；`CreateUser` 幂等 upsert、`VerifyPassword` 错误统一返 `ErrInvalidCredentials` 防邮箱探测）、`SessionStore`（Redis 服务端会话 `aigis:session:<id>`→JSON Principal，滑动 TTL 默认 24h，`crypto/rand` 16B id；选服务端会话而非 JWT 是为**即时登出/吊销** + 多副本共享）
- **双认证中间件**：`ee/auth` `SessionMiddleware`（同一 `server.Middleware`）先认 session cookie、后退回 Bearer API key——**程序化客户端零改动**，人登录 UI 管理 key、程序拿 key 打网关；`POST /login`（验密→建会话→下发 `aigis_session` HttpOnly/SameSite=Lax cookie）、`POST /logout`（服务端删会话 + 清 cookie）、`GET /me`（当前 principal）；`/login`/`/logout`/`/me` 进 `skipPaths`（无会话库时 `/me` 落 catch-all 200，UI 据此退回 token 模式而非死登录页）
- **接线 + 种子**：`serve.go` DSN 分支下，配 `ee.auth.session.redis_addr`（或复用 `ee.quota.redis_addr`）则建 `UserStore`+`SessionStore`、按 `ee.auth.users` 幂等 upsert 种子用户、`srv.Use(SessionMiddleware)`；未配则退回 `auth.Middleware`（纯 API key）
- **UI 登录页**：`index.html` init 探 `/me`——401→登录表单、有 `subject`→登录态（显示 whoami/Logout、隐藏 token 框）、其它→API-key 模式（token 框）；fetch 全带 `credentials:"include"`
- **测试**：`session_middleware`（接口 seam `sessionAPI`/`userAPI` + 内存 fake，7 例：登录发 cookie/错密码 401/无凭证 401/登出失效/`/me`/Bearer 退回）、`users`（`normalizeEmail`、bcrypt 往返）无库单测全过；CORE_CLEAN（bcrypt/pgx/redis 不漏进核心）
- **真机 e2e（PG+Redis，11/11）**：无凭证 /admin/keys 401 → 错密码 login 401 → 正确 login 200 发 cookie → 带 cookie /me 出 subject → 带 cookie /admin/keys 200 → Bearer key /admin/keys 仍 200 → /logout 204 → 同 cookie 再访问 401

### SaaS — 租户数据隔离（batch A 之上，batch B）
- **两级 admin**：`ee/auth` `EffectiveTenant(ctx, platformTenant) (scope, isPlatform)`——principal.tenant == `ee.auth.platform_tenant`（默认 `ops`）为**平台 admin**（`scope=""` 不过滤、全租户可见），其余为**租户 admin**（`scope=本租户`，无视用户传入 `?tenant=`，只见/只动本租户）；平台租户名可配（一个纯函数扛全部隔离判定，DB-free 单测覆盖）
- **keys 隔离**：`/admin/keys` list 按 `EffectiveTenant` 覆写查询 tenant（租户 admin 忽略 `?tenant=`）；create **强制改写** `req.Tenant` 为本租户（租户 admin 越权建 globex key 落到自己 acme，非 403，无声纠偏）；revoke 先 `KeyTenant(rawKey)` 查 key 归属，跨租户吊销返 **403**
- **audit 隔离**：`AuditFilter.TargetTenant` + `ListAudit` SQL 加 `($N='' OR target_tenant=$N)`；`RevokeKey` 改用 `UPDATE ... RETURNING tenant` 捕获被吊销 key 的租户写入审计行——**零新迁移**（审计隔离是 SQL 条件 + RETURNING，不加列）；租户 admin 看审计只见本租户行
- **usage 隔离**：`ee/billing` `handleUsage` 同走 `auth.EffectiveTenant` 收敛 `UsageQuery.Tenant`
- **接线**：`serve.go` `platformTenant()`（读 `ee.auth.platform_tenant`，默认 `ops`）串入 `auth.AdminMiddleware` + `billing.AdminMiddleware`；核心不感知（CORE_CLEAN）
- **真机 e2e（PG+Redis，10/10）**：平台 admin 见 acme+globex 且 `?tenant=` 过滤生效 → 租户 admin `?tenant=globex` 不泄漏、只见本租户 → 越权 create 被改写进本租户（globex→acme）→ 跨租户 revoke 403、本租户 revoke 204 → 审计无跨租户泄漏 → usage 200

### SaaS — 自助注册（batch C1）
- **`POST /register` 自助注册**：`ee/auth` `UserStore.RegisterUser(ctx, email, password, tenant)`——复用 users 表（零新迁移），bcrypt 哈希，`INSERT ... ON CONFLICT DO NOTHING`（**非 upsert**，绝不覆盖已有账号密码/租户/权限）；email 已存在返 `ErrEmailTaken`；`is_admin` **硬编码 false**（自助注册永远是普通成员，admin 只来自 bootstrap 或平台 admin）
- **开关默认关**：`ee.auth.allow_register`（默认 `false`，防垃圾注册）；关闭态 `/register` 直接 **404**（不暴露端点存在），`SessionMiddleware` 加 `allowRegister bool` 参数门控；`serve.go` `allowRegister()` 接线
- **handleRegister**：只接受 POST；缺字段 400、成功 **201**（返 `{tenant,subject}`，**不自动登录**，引导去 /login，KISS）、email 冲突 **409**（注册须暴露此唯一情形，与 login 的统一模糊错误不同，好让用户知道去登录）
- **测试**：`register_test`（扩展内存 fake `userAPI` 加 `RegisterUser`，4 例：关闭 404/缺字段 400/成功 201 且强制非 admin/冲突 409）DB-free 全过；CORE_CLEAN（不引新依赖）
- **真机 e2e（PG+Redis，8/8）**：关闭态 /register 404 → 开启缺字段 400 → 注册 201 → 重复 409 → 注册用户能 /login 200 发 cookie → 该用户普通成员访问 /admin/keys 403 → /me 显示 `admin:false`
- **不做（留 C2）**：邮箱验证/激活、密码重置、owner/member 角色细分、自动开租户——均强依赖 SMTP 发信基建，属另一批

### 配额维度扩展 — QPS（并发之上再加速率闸）
- **内存版 QPS**：`ee/quota` `RateLimiter`——每租户**固定窗口**（1 秒，按墙钟整秒对齐，边界处最多 2× 突发）请求数上限；`limitFor` per-tenant 覆盖 / default，0=不限；`now func()` 可注入，窗口滚动**无 sleep** 确定性单测
- **分布式 QPS**：`RedisRateLimiter`——Lua `INCR`+`EXPIRE 2`（秒折进 key，每窗口独立计数、到期自灭，无需显式 reset）原子跨副本；镜像并发 `RedisLimiter` 的 **fail-open**（Redis 挂了放行，绝不因 Redis 拖垮网关）
- **复合闸 `TenantLimiter`**：把「QPS 闸 + 并发闸」两个**独立可选**维度组合成单个核心 `quota.Limiter`（复用既有 `SetQuotaLimiter` seam，**核心零改动**）；`Acquire` **先查 QPS**——QPS 拒的请求**不预占并发 slot**（被限流的租户不会泄漏 in-flight 计数），过了 QPS 才查并发；`Release` **只释放并发**（速率额度靠时间回收，不靠请求完成）；任一闸 nil 即禁用该维度
- **接线**：`serve.go` `qpsConfig()`（读 `ee.quota.qps_default` / `qps_per_tenant`，镜像 `quotaConfig`）；并发/QPS 任一开启即组装，`ee.quota.redis_addr` 有则各闸走分布式、否则内存；日志一行同报两维度模式与上限
- **测试**：`rate_test`（per-tenant 天花板/窗口重置/default/unlimited/租户隔离，注入时钟）+ `composite_test`（QPS 拒不占并发 slot、并发拒、两闸都过 Release 释放、单闸 nil 另一个、两闸皆 nil 全放行）DB-free 全过（`-race`）；CORE_CLEAN
- **真机 e2e（内存 + Redis 各 2/2）**：`qps_default=2`，同一秒内 3 请求 → 2×200 + 1×429 → sleep 到下一窗口 → 再放行 200；内存版与 Redis 版行为一致

---

## 待办（TODO / 观察项，按需）

- [ ] **用量不丢的强保证（WAL）**：当前突发过载/DB 长挂仍会丢弃（有计数、不静默）；若计费要求「一条不丢」，需落盘缓冲（WAL / 磁盘队列）重放——重量级，按需再上
- [ ] **配额维度扩展 — token 配额**：并发 + QPS 已上；剩 token 配额（需读用量库累计消耗、跨窗口，重量级），按需再上
- [ ] **API key 近实时失效（pub/sub）**：现为轮询刷新（默认 30s 收敛，有界延迟）；若吊销需秒级跨副本生效，可加 Redis pub/sub 广播失效事件（各副本收到即 Reload）——引入 auth→redis 依赖 + 断连重订阅，按需再上
- [ ] **SaaS batch C2 — 邮箱验证 + 角色细分 + 密码重置**：自助注册（C1）已上；剩邮件验证码激活、租户内 owner/member 角色、密码重置流程——均强依赖 SMTP 发信基建，按需再上

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
    reload_interval_sec: 30  # 多副本下后台刷新 key 快照间隔秒（默认 30，≤0=禁用/单副本）
    platform_tenant: "ops"   # 平台 admin 所属租户名（默认 ops）：该租户 admin 全租户可见/可管；其余租户 admin 只限本租户
    allow_register: false    # 自助注册开关（默认 false）：true 才暴露 POST /register，注册用户强制普通成员(非 admin)
    session:               # 设置 redis_addr 后启用 SaaS 用户登录（/login /logout /me）
      redis_addr: ""       # 会话存储 Redis；留空则复用 ee.quota.redis_addr；都空=纯 API key
      redis_password: ""
      redis_db: 0
      ttl_hours: 24        # 会话滑动过期小时数（0/未设=默认 24）
    users:                 # 启动幂等 upsert 的仪表盘登录用户（bcrypt 哈希）
      - email: "admin@acme.io"
        password: "change-me"
        tenant: "acme"
        admin: true
  billing:
    dsn: "postgres://..."   # 亦可用环境变量 AIGIS_EE_BILLING_DSN（优先，密码不落盘）
    queue_size: 4096        # 异步写队列容量（0=默认 4096）
    batch_size: 100         # 每次入库批量条数（0=默认 100）
    flush_interval_ms: 2000 # 定时刷盘间隔毫秒（0=默认 2000）
    max_retries: 3          # 批量写失败退避重试次数（0=默认 3；负数=不重试）
  quota:
    default: 0              # 每租户并发上限（in-flight），0=不限
    per_tenant:            # 单租户并发覆盖
      tenant-a: 10
    qps_default: 0         # 每租户 QPS（每秒请求数，固定窗口）上限，0=不限
    qps_per_tenant:       # 单租户 QPS 覆盖
      tenant-a: 50
    redis_addr: ""         # 设置后并发+QPS 均跨副本共享（分布式）；否则单进程内存
    redis_password: ""
    redis_db: 0
```

> `ee.billing.dsn`（或 `AIGIS_EE_BILLING_DSN`）同时作为 auth 注册表与用量库的**共享 DSN**：设置后 `/admin/keys` + `/admin/usage` 启用、API key 走 DB；未设置则退回内存 + 静态 config key。

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

### SaaS — 邮箱验证 + 密码重置 + 角色管理（batch C2）
- **SMTP 基建（零新依赖）**：`ee/auth` `Mailer` 接口（`Send`/`Enabled`）+ `SMTPConfig`；标准库 `net/smtp`+`crypto/tls`——端口 465 隐式 TLS（`tls.DialWithDialer` 直连）、587/其他明文拨号后 `STARTTLS` 升级；`Host` 空返 `noopMailer`（`Enabled()=false`，邮件相关流程整体不挂载，优雅降级）；凭证只走 env（`AIGIS_SMTP_HOST/PORT/USER/PASSWORD/FROM`，永不落库/落配置）
- **一次性令牌表**：`migrations/005_email_tokens.sql`——单表 `email_tokens`（token PK / email / purpose / expires_at）服务两条流，`purpose`（`verify`|`reset`）区分防重放；令牌 128bit 随机（如同 session id 视为机密、绝不记日志），消费即删（单次），`expires_at` 界定寿命（默认 2h）
- **邮箱验证激活**：`UserStore.RegisterUserPending`（复用私有 `registerUser(enabled)` 助手，`enabled=FALSE` 建号）；`GET /verify?token=` `ConsumeToken`→`ActivateUser`（`enabled=TRUE`，幂等）；`VerifyPassword` 本就只放行 `enabled=TRUE`，未验证账号无法登录；`ee.auth.email_verify` 开关（默认 `false` 保留 C1 即时激活）
- **密码重置**：`POST /forgot {email}` **恒返 200**（不暴露邮箱是否注册）+ 有则发重置链接；`POST /reset {token,password}` `ConsumeToken`+bcrypt `UpdatePassword`；**不强制吊销存量会话**（无 email→session 索引，避免 SCAN 全量；新密码即时生效、旧密码不再认证——KISS/YAGNI）
- **角色管理**：`POST /admin/users/role {email,admin}` 把 owner/member **映射到既有 `is_admin` 标志**（admin=owner，member=普通），**零 schema 变更**；租户 admin 经 `EffectiveTenant` 限于本租户，平台 admin 跨租户；`SetUserAdmin(email, tenantScope, admin)` 带租户过滤为隔离边界
- **挂链顺序（关键）**：`EmailMiddleware`（公开流 /register-if-verify、/verify、/forgot、/reset，**token 验证、在 auth 之前**）→ `SessionMiddleware`（认证）→ `RoleMiddleware`（需 admin Principal、**在 auth 之后**，仿 `AdminMiddleware` 挂 /admin/keys）→ `AdminMiddleware`；C1 的 `SessionMiddleware` 签名与其 11 处测试调用点**完全不动**（另立两个中间件而非拓宽既有接口）
- **测试**：`email_flows_test`（内存 `emailUserAPI` fake + 捕获型 `Mailer` fake，10 例：注册验证 202→激活 200/无效令牌 400、verify 关时 /register 透传、/forgot 恒 200、重置 200+令牌单次 400、缺密码 400、角色非 admin 403/租内晋升 200/跨租 404/平台 admin 任租 200）DB-free 全过；CORE_CLEAN
- **真机 e2e（PG+Redis+SMTP，12/12）**：注册 202（真实发信）→ 未验证登录 401 → DB 取 token /verify 200 → 验证后登录 200 → /forgot 200 → DB 取 token /reset 200 → 旧密码 401、新密码 200 → 重放重置令牌 400 → admin 登录晋升测试用户 200 且 DB `is_admin=t` → 无 cookie 改角色 401

### API key 秒级失效 — Redis pub/sub（轮询之上再加广播）
- **发布端**：`ee/auth` `PostgresAPIKeyProvider` 加可选 `pub *redis.Client`（`SetPublisher` 注入，nil=不广播）；`CreateKey`/`RevokeKey` 本地 `Reload` 成功后 `Publish` 一条 `aigis:auth:keychange` 通知——发布失败**不回滚**（key 已改成功）、fail-loud 记日志，轮询仍是安全网
- **订阅端**：`StartSubscribe(ctx, rdb)` 后台 goroutine 订阅该 channel，收到任意消息即触发 `refreshFn`（= `Reload`）——消息内容只是提示，收端永远重载全量快照（不信任消息数据）；go-redis `Channel()` 内部自动重连，Redis 瞬断自愈；`done`/`wg` 管生命周期，`Close` 一并关订阅+pub 连接
- **与轮询并存**：pub/sub 求快（秒级）、`StartRefresh` 轮询兜底（消息丢/Redis 瞬断也最终一致）；`ee.auth.pubsub`（默认 false）开关，复用 session/quota 的 Redis 端点
- **接线**：`serve.go` `pubsubEnabled()`；DSN 分支下开启+有 Redis 则 `NewKeyChangeRedis` 建客户端、`SetPublisher`+`StartSubscribe`；无 Redis 则告警退回纯轮询
- **测试**：`pubsub_test`（nil publisher publish 无 panic 安全、`SetPublisher` 开关、nil client `StartSubscribe` 不起 goroutine/`wg` 不阻塞）Redis-free 全过（`-race`）；CORE_CLEAN
- **真机双副本 e2e（4/4）**：A/B 共享 DB+同 Redis、`reload_interval_sec=300`（轮询在窗口内基本不触发）→ B 接受 victim key(200) → A DELETE 吊销(204) → **B 于 0.01s 内跟随失效(401)**，证明是 pub/sub 秒级而非 300s 轮询

### 配额维度扩展 — QPS（并发之上再加速率闸）
- **内存版 QPS**：`ee/quota` `RateLimiter`——每租户**固定窗口**（1 秒，按墙钟整秒对齐，边界处最多 2× 突发）请求数上限；`limitFor` per-tenant 覆盖 / default，0=不限；`now func()` 可注入，窗口滚动**无 sleep** 确定性单测
- **分布式 QPS**：`RedisRateLimiter`——Lua `INCR`+`EXPIRE 2`（秒折进 key，每窗口独立计数、到期自灭，无需显式 reset）原子跨副本；镜像并发 `RedisLimiter` 的 **fail-open**（Redis 挂了放行，绝不因 Redis 拖垮网关）
- **复合闸 `TenantLimiter`**：把「QPS 闸 + 并发闸」两个**独立可选**维度组合成单个核心 `quota.Limiter`（复用既有 `SetQuotaLimiter` seam，**核心零改动**）；`Acquire` **先查 QPS**——QPS 拒的请求**不预占并发 slot**（被限流的租户不会泄漏 in-flight 计数），过了 QPS 才查并发；`Release` **只释放并发**（速率额度靠时间回收，不靠请求完成）；任一闸 nil 即禁用该维度
- **接线**：`serve.go` `qpsConfig()`（读 `ee.quota.qps_default` / `qps_per_tenant`，镜像 `quotaConfig`）；并发/QPS 任一开启即组装，`ee.quota.redis_addr` 有则各闸走分布式、否则内存；日志一行同报两维度模式与上限
- **测试**：`rate_test`（per-tenant 天花板/窗口重置/default/unlimited/租户隔离，注入时钟）+ `composite_test`（QPS 拒不占并发 slot、并发拒、两闸都过 Release 释放、单闸 nil 另一个、两闸皆 nil 全放行）DB-free 全过（`-race`）；CORE_CLEAN
- **真机 e2e（内存 + Redis 各 2/2）**：`qps_default=2`，同一秒内 3 请求 → 2×200 + 1×429 → sleep 到下一窗口 → 再放行 200；内存版与 Redis 版行为一致

### 配额维度扩展 — token 配额（并发/QPS 之上再加「每周期累计 token 上限」）
- **事件时间闸门（A1，不预占）**：token 数**响应后才知道**，故 `Acquire` 只做**只读**判断——「本周期已用 ≥ 上限？」→ 是则 429、否则放行；放行的请求**不预扣** token，实际消耗由响应后的用量 sink 记账。允许**一次轻微超额**（末个请求把已用推过上限），换取零预留的简单计数
- **内存版 `TokenLimiter`**：每租户**固定窗口**（`day`/`hour`/`month`，UTC 周期起点对齐，`periodBounds` 共享给内存/Redis 两版），`Allow`（已用<上限）+ `Add`（记账累加）实现 `TokenGate`；`limitFor` per-tenant/default，0=不限；`now func()` 可注入确定性单测
- **分布式版 `RedisTokenLimiter`**：key 折进周期起点秒（`aigis:quota:tokens:<tenant>:<periodStart>`，到期自灭）；`Allow`=GET 对比上限、`Add`=Lua `INCRBY`+**首写设 EXPIRE 到周期末**；镜像其余 Redis 闸的 **fail-open**（Redis 挂了 Allow 放行、Add 静默丢）
- **写半边 `TokenMeteringSink`（核心零改动）**：装饰核心 `usage.Sink`——`Record` 先转发给内层计费 sink，再 `token.Add(tenant, TotalTokens)`；核心本就在响应后调 `usageSink.Record`（此时 TotalTokens 已知），故**包一层 sink 即完成集成**，经 `SetUsageSink` 注入。读半边（`Allow`）走 `TenantLimiter` 的 `Acquire`，二者分离
- **复合闸扩展**：`TenantLimiter` 加**第三个可选** token 闸，`Acquire` 顺序 **QPS → token → 并发**（QPS/token 拒的请求都不预占并发 slot）；token 闸只在 `Acquire` 读、不在此记账
- **接线**：`serve.go` `tokenConfig()`（读 `ee.quota.token_default` / `token_per_tenant` / `token_period`）；用量 sink **改在 quota 块之后安装**——token 闸开启则用 `TokenMeteringSink` 包裹计费 sink 再 `SetUsageSink`
- **测试**：`token_test`（累计到上限拒、跨周期重置 day/hour、per-tenant 隔离、unlimited 不计、Add 翻转 Allow、`ParsePeriod`）+ `composite_test`（token 拒不占并发 slot、Acquire 不记账、QPS 拒不查 token）+ `usage_sink_test`（转发内层 + 按 TotalTokens 记账、0 token 不记、nil 闸仍转发）DB/Redis-free 全过（`-race`）；CORE_CLEAN
- **真机 e2e（内存 + Redis 各 4/4）**：`token_default=40`、快上游每次回 `total_tokens=18` → 逐个请求（间隔 0.4s 让 deferred 记账落定）：200(0<40)、200(18<40)、200(36<40)、429(54≥40)；内存版与 Redis 版行为一致

### 用量不丢的强保证 — WAL（磁盘预写日志 + 重放）
- **两个丢弃点兜底**：`PostgresSink` 原有两处「计数但丢弃」——`Record` 队列满（`DroppedFull`）、`writeBatch` 重试耗尽（`DroppedWrite`）；WAL 是**旁路兜底**：正常路径（异步队列→批量入库）完全不变，只有**本会被丢**的事件落盘，后台重放待 DB 恢复后补写，得**至少一次**投递
- **`WAL` 类型（`ee/billing/wal.go`）**：事件以 JSONL 追加到活跃段 `usage.wal`（`O_APPEND|O_CREATE 0o600`+互斥+fail-loud，镜像 audit.go 打法；含租户/token/请求 ID，owner-only）；超 `maxSegBytes` 轮转为纳秒戳 `usage-<nanos>.seg`（字典序=年龄序）；**惰性开文件**——空闲(从不溢出)的 sink 零磁盘触碰；损坏行（崩溃截断）跳过并告警，不拖累整段
- **幂等键 `(request_id, ts)`**：`usage_events` 是 Timescale 超表，唯一索引**必须含分区列 ts**；WAL 存 `WALRecord{TS, Event}` **保留原始记账时刻**，重放用 `insertAtSQL` 显式 ts 补写（非重放时刻），使实时写与其后重放**碰撞同一唯一索引**→ `ON CONFLICT DO NOTHING` 去重、绝不重复计费；空 request_id 走**部分索引**（`WHERE request_id <> ''`）排除、照常插入（保持 pre-WAL 行为）
- **重放环 `replayLoop`/`replayOnce`**：仅 WAL 开启时起后台 goroutine，按 `replayInterval` 扫；`replayOnce` 先 `Rotate` 活跃段（buffered 事件转为可重放 `.seg`）→ 列 pending 段（旧→新）→ 逐段 `ReadSegment`→`writeRecordsAt` 显式 ts 补写→成功才 `RemoveSegment`；**DB 仍挂则保留整段**下轮再试（不重复入库靠 ON CONFLICT，不 hammer 挂掉的 DB）；关停时 `s.done` 触发**最后一次** replayOnce 兜底刚落盘的事件
- **接线**：`serve.go` `billingOptions()` 读 `ee.billing.wal.dir`（**空=禁用，默认关**，YAGNI 零行为变更）/ `wal.max_seg_mb` / `wal.replay_interval_sec`；migration `002_usage_request_id_unique.sql`（**只写不跑**，用户手动 apply）建部分唯一索引
- **测试**：`wal_test`（追加→轮转→读回、损坏行跳过、空 Rotate no-op、Close 不轮转留活跃文件、RemoveSegment）+ `reliability_test` 扩展（Record 满队列落 WAL、writeBatch 耗尽整批落 WAL）DB-free 全过（`-race`）；CORE_CLEAN。修了 `Append` 轮转后未重开文件的 bug
- **真机 e2e（TimescaleDB，PASS）**：TCP 代理挡在真 DB 前 → phase1 三请求正常入库 → **kill 代理(DB 不可达)** 再三请求 → 重试耗尽落 WAL（生成 1 段 3 条）→ **重启代理(DB 恢复)** → 重放环补写 → `/admin/usage` 口径行数=6、tokens=108；**二次 sweep 行数仍 6**（ON CONFLICT 幂等，不重复计数）

---

## 待办（TODO / 观察项，按需）

- [x] **用量不丢的强保证（WAL）**：已上——`ee.billing.wal.dir` 开启后，本会被丢的用量（队列满 / 批量写重试耗尽）落盘 JSONL 段，后台重放环待 DB 恢复补写；幂等键 `(request_id, ts)`（保留原始记账时刻 + `ON CONFLICT DO NOTHING`）不重复计费；默认关（YAGNI 零行为变更）；真机 e2e 坏端口模拟 DB 不可达→恢复→重放，行数/tokens 精确、二次 sweep 不重复
- [x] **配额维度扩展 — token 配额**：已上——`ee.quota.token_default`/`token_per_tenant`/`token_period`（day/hour/month）；事件时间闸门（响应后记账、不预占，允许一次轻微超额），`TokenMeteringSink` 包裹计费 sink 记账（核心零改动），内存/Redis 双后端；真机 e2e 内存+Redis 各 4/4
- [x] **API key 近实时失效（pub/sub）**：已上——`ee.auth.pubsub` 开启后 Redis 广播 key 变更，各副本秒级 Reload（轮询兜底并存）；真机双副本 e2e 0.01s 跟随失效
- [x] **SaaS batch C2 — 邮箱验证 + 角色细分 + 密码重置**：SMTP 基建（`net/smtp` 465/587，`noopMailer` 降级）+ `email_tokens` 一次性令牌表；邮箱验证激活（`RegisterUserPending`→`/verify`）、密码重置（`/forgot` 恒 200 + `/reset`）、owner/member 角色映射到 `is_admin`（`/admin/users/role`，`EffectiveTenant` 隔离）；DB-free 单测 10 例 + 真机 PG+Redis+SMTP e2e 12/12 全过

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
    pubsub: false            # key 变更 Redis pub/sub 秒级跨副本失效（默认 false）：true 且有 Redis 才广播；轮询始终兜底
    platform_tenant: "ops"   # 平台 admin 所属租户名（默认 ops）：该租户 admin 全租户可见/可管；其余租户 admin 只限本租户
    allow_register: false    # 自助注册开关（默认 false）：true 才暴露 POST /register，注册用户强制普通成员(非 admin)
    email_verify: false      # 邮箱验证开关（默认 false=即时激活）：true 且配了 SMTP 时，/register 建禁用账号+发验证信，须 /verify 后方可登录
    dashboard_url: ""        # 外部可达的仪表盘 origin（如 https://app.example.com），用于拼验证/重置邮件里的链接；留空=相对路径
    # SMTP 发信（C2 邮箱验证/密码重置）走环境变量，永不落配置：
    #   AIGIS_SMTP_HOST / AIGIS_SMTP_PORT(465隐式TLS|587 STARTTLS) / AIGIS_SMTP_USER / AIGIS_SMTP_PASSWORD / AIGIS_SMTP_FROM
    #   HOST 留空=noopMailer，/verify /forgot /reset /admin/users/role 均不挂载（优雅降级）
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
    wal:                    # 用量不丢的磁盘预写日志（默认关：dir 留空=禁用，count-and-drop 老行为）
      dir: ""               # WAL 目录，非空即开启：队列满/批量写耗尽的事件落盘、DB 恢复后重放
      max_seg_mb: 16        # 活跃段超此大小即轮转为待重放 .seg（0=默认 16MB）
      replay_interval_sec: 30 # 后台扫 WAL 回灌 DB 的间隔秒（0=默认 30）；需先 apply migration 002 建 (request_id,ts) 唯一索引
  quota:
    default: 0              # 每租户并发上限（in-flight），0=不限
    per_tenant:            # 单租户并发覆盖
      tenant-a: 10
    qps_default: 0         # 每租户 QPS（每秒请求数，固定窗口）上限，0=不限
    qps_per_tenant:       # 单租户 QPS 覆盖
      tenant-a: 50
    token_default: 0       # 每租户「每周期累计 token」上限，0=不限（事件时间闸门，允许一次轻微超额）
    token_per_tenant:     # 单租户 token 上限覆盖
      tenant-a: 1000000
    token_period: "day"   # token 配额周期：day（默认）| hour | month，UTC 周期起点对齐
    redis_addr: ""         # 设置后并发+QPS+token 均跨副本共享（分布式）；否则单进程内存
    redis_password: ""
    redis_db: 0
```

> `ee.billing.dsn`（或 `AIGIS_EE_BILLING_DSN`）同时作为 auth 注册表与用量库的**共享 DSN**：设置后 `/admin/keys` + `/admin/usage` 启用、API key 走 DB；未设置则退回内存 + 静态 config key。

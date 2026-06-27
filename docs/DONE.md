# DONE — 已完成功能与变更记录

> 本文件是工程变更日志（已落地项）。待办见 [`TODO.md`](TODO.md)。

---

## 2026-06-27

### 新增 gemini provider 路由（OpenAI 兼容端点，零翻译）
- `config.yaml` 加 `gemini-default` 路由：matcher `^gemini-.*`，upstream `env:AIGIS_GEMINI_BASE_URL`(/v1 OpenAI 兼容端点) + `path: /chat/completions` + bearer + `token_env: GEMINI_API_KEY`，transform 仅 pii
- 选 OpenAI 兼容端点而非 gemini 原生（contents/parts）：请求/响应进出都是 OpenAI 形态，**无需协议翻译**（不像 dify）；流式走默认透传 unmask 即可（上游回 OpenAI 格式 SSE）
- `.env` 加 `AIGIS_GEMINI_BASE_URL`（OpenAI 兼容端点，可被 e2e 覆盖）
- 现共 5 条路由：openai / gemini / claude(glm) / dify / fallback
- 真机 e2e（网关 + 真实 inferera 上游，`tmp/test_gemini_real.sh`）4/4：非流式 200+OpenAI 形态+PII 往返还原、流式 OpenAI chunk+[DONE]、命中 gemini-default
  - 定位插曲：上游先后换 aihubmix→inferera、旧 key `this key is not enabled`(401，gpt 也复现，证明是 key 非代码) → 换新 key 后全通；集成代码零改动

### 脱敏规则配置化 (security.custom_rules)
- `security.CustomRule{Name,Pattern}` + `NewScannerWithRules([]CustomRule)`：内置规则 + 用户自定义规则，自定义正则**启动期编译+校验**，空名/坏正则响亮报错
- `config.LoadCustomRules()` 读 `config.yaml` 的 `security.custom_rules`；匹配项与内置规则一样走 mask→vault→unmask 全链路
- 架构改进：scanner 无状态（vault 在 ctx），改为**启动建一次共享**（`HTTPServer.scanner` 注入 provider），消除原「每请求重建 scanner + 重编译正则」的浪费；孤儿方法 `AddRule` 正式接到配置
- 测试：`NewScannerWithRules`（合法 roundtrip / 坏正则 / 空名）+ universal_test 适配；真机 e2e 抓包——身份证规则生效，原文发往上游前已 tokenize

### 移除死的 Pipeline/Processor 中间件层
- `Pipeline` 永远只装 `RequestLogger`，而它从不改 body（纯间接层）；转换逻辑早已在 transform 策略 + `UniversalProvider`
- 删除 `pipeline.go`/`processor.go`/`processors` 整包；`http.go` 日志改内联（"Request started"）+ defer 收尾（"Request finished"）
- 顺带修两个潜藏 bug：日志 path 不再硬编码（取 `r.URL.Path`，`/v1/messages` 正确）；流式请求现也记录收尾日志（旧实现只在非流式跑且 always Success）
- 净删约 310 行；build/test 全绿；真机冒烟确认

### 邮箱脱敏可选策略（保留域名）
- `security.MaskOptions{EmailMode}`：`full`(默认,整封替换) / `local`(只替换 @ 前的 mailbox,保留 @domain)
- `Mask` 保持默认包装零破坏；email-local 按 `@` 切分,vault 只存 local,Unmask 自动重组完整邮箱(占位符紧贴 @domain，故 unmask/流式/审计均无需改)
- 配置驱动:PII transform 读 `config.email`;`config.yaml` dify 路由设 `email: "local"`
- 测试:scanner(full/local/子域名/unmask 还原) + pii transform(config 驱动);真机双证——echo 上游抓包确认发往上游为 `占位符@example.com` + 真实 dify 流式往返完整还原邮箱无泄漏

### dify 流式（SSE）响应翻译 dify→OpenAI
- 请求模板 `response_mode` 按客户端 `.stream` 切换（`{{if .stream}}streaming{{else}}blocking{{end}}`），dify 路由不再强制 blocking
- 新增 `transform.DifyStreamTranslator`（实现 `StreamTransformer`，Adapter）：dify SSE 事件流 → OpenAI `chat.completion.chunk` 流。`message`/`agent_message`→delta（首块带 role）、`message_end`→stop 块 + `[DONE]`；`ping`/`workflow_*`/`node_*`/`tts_message` 等丢弃；Flush 兜底保证上游中断也收尾
- 抽出 `carryUnmask`（组合，非继承）：占位符跨事件重组逻辑由 `StreamUnmasker` 与新 translator 共用，避免分叉
- 配置驱动选择：路由加 `stream_translate`（dify 路由设 `"dify"`），`transform.NewStreamTransformer(name)` 工厂；空/`unmask` 仍走原透传（行为不变）
- validate 新增 `stream_translate` 已知值校验（注入 `transform.KnownStreamTranslators()`，坏值启动期报错）
- 测试：`dify_stream_test.go`（基础翻译/丢弃非答案事件/占位符跨两 message 还原/无 message_end 兜底）；真机 dify 流式往返 6/6

---

## 2026-06-26 — 工程健壮性三件套

### 1. 配置验证层
- `EngineConfig.Validate(knownTransforms, knownStreamTranslators)`（`internal/core/engine/validate.go`）：路由非空、ID 唯一非空、matcher 正则合法、upstream base_url 非空、auth_strategy 合法（含 none/空）、transform 类型已知、stream_translate 已知、必须存在 catch-all（空 matcher）路由
- 在 `server.NewHTTPServer` 加载配置后、建 engine 前调用，**坏配置启动期即响亮报错**而非运行期误路由
- transform 合法类型由 `transform.KnownTypes()` 注入，保持 engine 不反向依赖 transform 实现
- 测试：`validate_test.go`（OK 用例 + 失败模式表驱动 + none/空 auth）

### 2. dify / 多 provider 路由打通（已对真实 dify 实测 200）
- `config.yaml` 启用 dify 路由，端点 `/chat-messages`（dify 是 chat/chatflow 类型，advanced-chat 模式）
- 变量名用 `env:AIGIS_DIFY_BASE_URL` + `AIGIS_DIFY_API_KEY`
- template transform 加 `json` 函数：JSON 转义 content，修复裸插值在内容含引号/换行时产出非法 JSON 的隐患
- 响应翻译：Route 新增 `response_transforms`（非流式响应先翻译后 unmask）；dify 原生响应 → OpenAI chat-completion 形态（object/choices/usage）
- 现共 4 provider 路由：openai-default / claude-proxy / dify(chat) / fallback
- 测试：`TestTemplateTransformDifyShape`（请求转义）+ `TestTemplateTransformDifyResponseToOpenAI`（响应翻译）

### 3. 日志滚动切割（可选，默认关闭）
- **opt-in**：`log.rotate` 默认 false = 单文件（交给系统 logrotate）；设 true 才启用内置 lumberjack
- `logger.go` 按 `RotationConfig.Enabled` 二分：关闭走 `buildStaticLogger`，启用走手工 lumberjack 双 core（`buildRotatingLogger`），编码器经 `configureEncoder` 共享
- 配置化：`config.yaml` `log.{rotate,file,max_size_mb,max_backups,max_age_days,compress}`
- 测试：`rotation_test.go` 临时目录隔离——启用时 MaxSize=1MB 强制滚动断言产生 backup；关闭时仅单文件不滚动

### 脱敏审计追踪 (NEXT#1)
- 新增 `internal/core/audit` 包：发生脱敏的请求向 `./logs/audit.jsonl` 追加一条 JSONL（`request_id/trace_id/timestamp/model/route_id/total/by_type/items/duration_ms`）
- 采集解耦：`Scanner.Mask` 命中时独立断言 `RecordDetection(type, placeholder, preview)`，不动 `vaultContext` 接口
- 记录粒度：**仅元数据 + 部分打码预览**（`maskPreview` 首2尾2中间`***`，≤4位全码）；**不落完整明文**；干净请求不写
- 写入时机：`handleGateway` 单个 `defer auditor.Record(ctx)` 统一覆盖流式/非流式
- 配置：`audit.enabled`（缺省 true）；文件权限 `0o600`
- fail-loud：marshal/write 失败 `log.Error` 带 request_id（不崩主请求但响亮留痕）
- 评审：`docs/CODE_REVIEW_audit_feature_2026-06-26.md`
- 验证：单测（audit 5 用例 + maskPreview 表驱动）+ 真实 GLM 端到端

### 并发监控 (NEXT#2)
- 新增 `internal/core/metrics` 包：`sync/atomic` 无锁计数 in_flight / peak_concurrency / total / success / failed + uptime（**仅监控，不限流** YAGNI）
- `handleGateway` 入口 `metrics.Begin()` + `defer End(succeeded)`，覆盖在途全生命周期
- 新增 `GET /metrics` 端点返回 JSON snapshot
- 踩坑修复：成功判定变量被 `flusher, ok :=` 遮蔽 → 改名 `succeeded`
- 验证：`go test -race` 并发单测（1000 goroutine）+ 真实 GLM 端到端

---

## 2026-06-25

### Claude Code 代理打通（问题1：ANTHROPIC_BASE_URL 收不到）
- 注册 `/v1/messages` 端点（与 `/v1/chat/completions` 共用 handler，按 model 路由）
- 实现 SSE 流式透传 `SendStream`（此前 `streaming not implemented` 正是"收不到"的根因）
- claude-proxy 路由实测打通：mask → 转发 → 流式 unmask 还原
- 已验证上游：GLM（智谱 `open.bigmodel.cn` anthropic 兼容端点），model `glm-4.6` / `glm-5.2`
  - `config.yaml` claude-proxy matcher 放宽为 `^(claude|glm).*`，认证走 `x-api-key`（`env:AIGIS_ANTHROPIC_KEY`）

### 消息回溯（往回替换）= 双向 tokenization (NEXT#3)
- 请求 Mask 入 vault、响应 Unmask 还原，OpenAI/Claude 双格式

### Bug 修复：流式 unmask 跨-chunk 失效
- 现象：占位符 `__AIGIS_SEC_xxx__` 被上游逐 token 拆到多个 SSE delta，逐行 unmask 无法重组 → 泄漏给客户端
- 修复：`transform.StreamUnmasker` 语义层重组（两层缓冲：SSE 事件 + 占位符前缀 carry），跨 delta 累积 unmask + splitSafe 前缀保留 + 空 delta 跳过
- 二次修复：`partialPlaceholderRe` hex 上限由 `{0,11}` 改为 `{0,12}_{0,2}`（完整 hash 12 位 hex，原正则在「hex 满 12 位、尾 `__` 未到」时漏匹配提前 flush 泄漏）
- 覆盖：transform 包单测（Claude/OpenAI 拆分 + splitSafe 边界）+ 端到端实测

### 架构重构：转换引擎 Strategy 化
- 新建 `internal/core/transform/`：Transformer 接口 + Registry，pii/field_map/template/unmask 各为独立策略
- universal.go 大 switch → 数据驱动分发（OCP，加新转换无需改 Provider）

### 配置/测试入口
- `~/.claude/models/aigis.json`：指向 `http://127.0.0.1:8080`，用 `cm aigis` / `cmy aigis` 启动真实 Claude Code 走 AIGis
- `tmp/test_claude_messages_stream.sh`：curl 流式 PII 回归脚本

### 日志落盘
- `internal/pkg/logger/logger.go`：输出同时写 stdout 和 `./logs/aigis.log`（自动建目录），`logs/` 已 gitignore

---

## 杂项（原 TODO.10 已解决项）
- ✅ **测试覆盖**：核心模块已有大量 `*_test.go`（security/transform/engine/server/audit/metrics/logger/config）
- ✅ **.gitignore**：`CLAUDE.md` 不再被忽略

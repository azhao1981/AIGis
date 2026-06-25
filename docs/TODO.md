
问题1：可以让claude code 使用做代理地址
  "ANTHROPIC_BASE_URL": "http://127.0.0.1:8080/v1", 后收不到



问题2：provider 添加 openai dify cluade

NEXT：
1. 记录 敏感信息  ✅ 已完成（2026-06-26）
2. 并发监控  ✅ 已完成（2026-06-26）
3. 消息回溯（往回替换）  ✅ 已完成

---

## 已完成 (2026-06-25)

### 问题1 解决：Claude Code 代理打通
- 注册 `/v1/messages` 端点（与 `/v1/chat/completions` 共用 handler，按 model 路由）
- 实现 SSE 流式透传 `SendStream`（此前 `streaming not implemented` 正是“收不到”的根因）
- claude-proxy 路由实测打通：mask → 转发 → 流式 unmask 还原
- 已验证上游：GLM（智谱 `open.bigmodel.cn` anthropic 兼容端点），model `glm-4.6` / `glm-5.2`
  - 配置见 `.env`（`AIGIS_ANTHROPIC_BASE_URL` / `AIGIS_ANTHROPIC_KEY`，ak 在 .env 不入库）
  - `config.yaml` claude-proxy matcher 放宽为 `^(claude|glm).*`，认证走 `x-api-key`（header_policy.set `env:AIGIS_ANTHROPIC_KEY`）

### NEXT#3 消息回溯（往回替换）= 双向 tokenization
- 请求 Mask 入 vault、响应 Unmask 还原，OpenAI/Claude 双格式

### Bug 修复：流式 unmask 跨-chunk 失效
- 现象：占位符 `__AIGIS_SEC_xxx__` 被上游逐 token 拆到多个 SSE delta，逐行 unmask 无法重组 → 占位符泄漏给客户端
- 修复：`transform.StreamUnmasker` 语义层重组（两层缓冲：SSE 事件 + 占位符前缀 carry），跨 delta 累积 unmask + splitSafe 前缀保留 + 空 delta 跳过
- 二次修复：`partialPlaceholderRe` hex 上限由 `{0,11}` 改为 `{0,12}_{0,2}`
  - 原因：完整 hash 是 12 位 hex，原正则在「hex 已满 12 位、尾 `__` 尚未到达」时匹配不到，会把整段当普通文本提前 flush 泄漏
  - 新增 splitSafe 边界用例（12hex 缺尾 / 12hex+1下划线）锁定，实测 GLM 流式确认还原
- 覆盖：transform 包单测（Claude/OpenAI 拆分用例 + splitSafe 边界）+ 端到端实测

### 架构重构：转换引擎 Strategy 化
- 新建 `internal/core/transform/`：Transformer 接口 + Registry，pii/field_map/template/unmask 各为独立策略
- universal.go 大 switch → 数据驱动分发（OCP，加新转换无需改 Provider）

### 配置/测试入口
- `~/.claude/models/aigis.json`：指向 `http://127.0.0.1:8080`，用 `cm aigis` / `cmy aigis` 启动真实 Claude Code 走 AIGis
- `tmp/test_claude_messages_stream.sh`：curl 流式 PII 回归脚本

### 日志落盘
- `internal/pkg/logger/logger.go`：输出同时写 stdout 和 `./logs/aigis.log`（自动 `os.MkdirAll` 建目录），`logs/` 已在 `.gitignore`
- 未做滚动切割（YAGNI）；常驻运行需再加 lumberjack 按大小/天数切割

### NEXT#1 记录敏感信息 = 脱敏审计追踪（2026-06-26）
- 新增 `internal/core/audit` 包：发生脱敏的请求向 `./logs/audit.jsonl` 追加一条 JSONL（`request_id/trace_id/timestamp/model/route_id/total/by_type/items/duration_ms`）
- 采集解耦：`Scanner.Mask` 命中时独立断言 `RecordDetection(type, placeholder, preview)`，不动既有 `vaultContext` 接口（现有 Mask 单测零破坏）
- 记录粒度：**仅元数据 + 部分打码预览**（`maskPreview` 首2尾2中间`***`，≤4位全码，如 `te***om`）；**不落完整明文**；干净请求不写
- 写入时机：`handleGateway` 单个 `defer auditor.Record(ctx)` 统一覆盖流式/非流式
- 配置：`config.yaml` `audit.enabled`（缺省 true）；文件权限 `0o600`（含 preview 部分明文+指纹，比 aigis.log 敏感）
- fail-loud：`Auditor` 注入 zap logger，marshal/write 失败 `log.Error` 带 request_id（不崩主请求但响亮留痕）
- 代码评审处置见 `docs/CODE_REVIEW_audit_feature_2026-06-26.md`
- 验证：单测（audit 5 用例 + maskPreview 表驱动）+ 真实 GLM 上游端到端（三类密钥脱敏、权限 600、无明文泄漏均实测确认）

### NEXT#2 并发监控（2026-06-26）
- 新增 `internal/core/metrics` 包：`sync/atomic` 无锁计数 in_flight / peak_concurrency / total / success / failed + uptime（监控，**不限流** YAGNI）
- `handleGateway` 入口 `metrics.Begin()` + `defer End(succeeded)`，覆盖在途全生命周期；流式/非流式成功路径置 `succeeded=true`，早返回错误路径默认 failed
- 新增 `GET /metrics` 端点返回 JSON snapshot
- **踩坑修复**：成功判定变量原名 `ok`，被 `if flusher, ok := w.(http.Flusher)` 的 `:=` 遮蔽，导致流式成功被误记 failed；改名 `succeeded` 解决（实测 success=0→正确计数）
- 验证：`go test -race` 并发单测（1000 goroutine，in_flight 归零、total/peak 正确）+ 真实 GLM 端到端（峰值在途 3、success/failed 分别计、非 POST 不计数均实测确认）


问题1：可以让claude code 使用做代理地址
  "ANTHROPIC_BASE_URL": "http://127.0.0.1:8080/v1", 后收不到



问题2：provider 添加 openai dify cluade

NEXT：
1. 记录 敏感信息
2. 并发监控
3. 消息回溯（往回替换）

---

## 已完成 (2026-06-25)

### 问题1 解决：Claude Code 代理打通
- 注册 `/v1/messages` 端点（与 `/v1/chat/completions` 共用 handler，按 model 路由）
- 实现 SSE 流式透传 `SendStream`（此前 `streaming not implemented` 正是“收不到”的根因）
- claude-proxy 路由实测打通（上游 inferera）：mask → 转发 → 流式 unmask 还原

### NEXT#3 消息回溯（往回替换）= 双向 tokenization
- 请求 Mask 入 vault、响应 Unmask 还原，OpenAI/Claude 双格式

### Bug 修复：流式 unmask 跨-chunk 失效
- 现象：占位符 `__AIGIS_SEC_xxx__` 被上游逐 token 拆到多个 SSE delta，逐行 unmask 无法重组 → 占位符泄漏给客户端
- 修复：`transform.StreamUnmasker` 语义层重组（两层缓冲：SSE 事件 + 占位符前缀 carry），跨 delta 累积 unmask + splitSafe 前缀保留 + 空 delta 跳过
- 覆盖：transform 包单测（Claude/OpenAI 拆分用例）+ 端到端实测

### 架构重构：转换引擎 Strategy 化
- 新建 `internal/core/transform/`：Transformer 接口 + Registry，pii/field_map/template/unmask 各为独立策略
- universal.go 大 switch → 数据驱动分发（OCP，加新转换无需改 Provider）

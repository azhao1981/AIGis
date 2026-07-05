# TODO — 待办

> 已完成项见 [`DONE.md`](DONE.md)。

## A. 工程门面 / 开源标配（非功能，低风险）
- [x] **LICENSE** — AGPLv3 双授权（LICENSE + COMMERCIAL.md + CLA.md）
- [x] **README** — 补充项目介绍 + 拆分中英双语（README.md / README.zh-CN.md）
- [x] **CI/CD** — 已加 `.github/workflows/ci.yml`（build + vet + test）
- [x] **CONTRIBUTING.md** — 已加中英双语贡献指南（CLA + PR 清单 + 提交规范）

## B. 功能方向（需确认是否要做 / 优先级）
- [x] **更多 provider 适配（兼容类）** — OpenAI 兼容清单已交付：config.yaml 注释态示例（DeepSeek/Qwen/Kimi）+ README 中英兼容表。剩余按需：
  - [x] Azure OpenAI legacy（`?api-version=` 拼进 path + `api-key` 头）— 纯配置，无需核心改动。config.yaml 注释态示例 + README/CONFIGURATION 中英文档 + tmp mock e2e 已 PASS
  - [x] Anthropic 原生 `/v1/messages`（`x-api-key` + `anthropic-version` 头，`pii_claude`）— claude-proxy 路由已支持，补 config/README/CONFIGURATION 说明
  - [x] Bedrock SigV4（AWS V4 签名：HMAC 密钥派生链 + canonical request + SignedHeaders），`auth_strategy: bedrock` 自动派发 Authorization / X-Amz-Date / X-Amz-Content-SHA256；httptest mock 验签 + AWS 公开 kSigning 测试向量回归
- [x] **脱敏增强（进阶）** — per-route 额外自定义规则（transform `custom_rules`，进程级编译缓存，不污染共享 scanner）
- [x] **流式强审核** — 路由 `force_block`：stream 请求内部降级为 blocking，脱敏后做 egress 泄露复检（内置规则漏网即拒发），客户端仍收到伪流式 SSE

## C. 入站防护（product positioning）

> **定位澄清**：AIGis 定位是**防止 PII / 敏感数据发出去**（入站 egress 管控），主要给 Claude Code 之类的 agent 使用。
> 因此**输出脱敏不做**——那是"防 LLM 返回什么"，与本产品"防发出去什么"的定位偏离，无实际价值。

- [x] **② Prompt 注入 / 越狱检测** — 入站 transform `injection`：内建启发式规则（`ignore previous instructions`、DAN、`system prompt` 泄露等），`mode: block` 命中即拒（走 transform error 路径，请求不发上游）/ `mode: warn` 只记 ctx metadata 告警；`extra_patterns` route 级追加正则（进程内编译，不污染共享状态）
- [x] **③ 请求体大小 / token 预算预检** — 入站 transform `guard`：`max_bytes` 超限即拒、`max_tokens` 超上限即拒，打上游前拦截，省掉无谓的上游花费

## D. 防护缺口（2026-07 评审，按与 egress 定位的相关度排序）

### P0 — 定位核心的真实防护缺口
- [x] **① Private Key 整块脱敏** — 现规则只匹配 `-----BEGIN ... PRIVATE KEY-----` 头部一行，密钥体（base64 块）原样发出。改为匹配 BEGIN…END 整块（含密钥体）。安全 bug 级，最优先。
- [x] **② 编码内容绕过（base64 二次扫描）** — scanner 只扫明文，PII/secret 塞进 base64 即穿透。对疑似 base64 块解码后二次扫描（命中即按原规则处置）。
- [x] **③ 通用高熵 secret 检测**（Anthropic/Slack/Stripe 平台 token + JWT 三段式带 JSON 校验 + 赋值型 key=value 泄露）

### P1 — 产品成熟度
- [x] **④ 审计查询入口**（`GET /admin/audit?limit=&rule=` + UI Masking 面板） — 脱敏审计只写 `audit.jsonl`，OSS 无查询面。加只读 `GET /admin/audit`（近 N 条 + 按规则过滤，仅元数据）+ UI Audit 面板——让用户看见网关拦了什么（产品价值展示面）。
- [x] **⑤ Claude Code 接入指引 + 部署物料**（Dockerfile + docker-compose + README 中英双语 Claude Code/Docker 章节）
- [x] **⑥ Prometheus 维度细化**（per-route 请求/失败计数 + 延迟直方图 + per-rule PII 命中计数 + injection/transform 拦截计数）
- [x] **⑦ 配置热加载**（SIGHUP → engine.routes + security.custom_rules 原子替换，fail-loud 不污染在线状态）

> 执行顺序：① → ② → ④ → ⑤ → ⑥；③⑦ 按需。

## E. 最佳实践补强（2026-07 review 后评估，按价值排序）

### 高价值
- [x] **① CI 补 fmt check** — 现有 ci.yml 只有 build/vet/test，gofmt 遗漏拦不住（840dad8 提交漏 fmt、由后续 review 才发现即是实证）。加一步 `test -z "$(gofmt -s -l .)"`。
- [x] **② audit.jsonl 轮转/保留** — 对齐应用日志的 rotation 约定（`audit.rotate` 默认关、lumberjack 按大小/天切割 + 保留 N 份）；`/admin/audit` 只查当前文件，切割出去的历史文件是保留策略。

### 中价值
- [x] **③ Fuzz 测试** — Go native fuzzing 覆盖 `Scanner.Sanitize/Mask`（正则 + base64 解码）与 `FindRoute`（sonic 解析不可信 body），panic 即拒绝服务，网关直面不可信输入。
- [x] **④ tool_calls 脱敏覆盖** — OpenAI `tool_calls[].function.arguments`（整串按文本脱敏，占位符不破坏 JSON）+ content:null 消息不再跳过；Claude `tool_use.input`（只脱敏字符串叶子，结构/数字保留）与 `tool_result`（字符串或嵌套块数组）。
- [x] **⑤ 真实上游 e2e smoke** — `tests/smoke_real_upstream.sh`：读 `.env` 决定跳过哪条链，发真实请求让上游模型回声种子 email，验证 mask→上游→unmask 往返（非 CI、手动触发；上游不可达 / 无 key 时 SKIP 而非 fail）。

### backlog（按需再做）
- [x] Bedrock SigV4（见 B 段）
- [x] **健康探针 / 上游可用性探测** — `/health` 被动聚合各路由 breaker 状态：任一非 `closed` 即 `status: degraded`，进程存活始终返回 HTTP 200（body 供 LB/readiness 判读），无主动探测 goroutine（零上游副作用）
- [x] Prometheus `/metrics/prometheus` 导出 — 手写文本格式（零依赖）：请求计数/在途/breaker 状态；维度细化见 D-⑥
- [x] **上游重试** — 全局 `retry`：瞬时失败（网络错误 / 429 / 5xx）线性退避重试（`backoff_ms * attempt`），4xx 立即返回；`max_attempts<=1` 默认关闭
- [x] **多 key 负载均衡** — `upstream.token_envs` 多 key 轮询（原子游标按 route ID 持久化，跳过空值 env，回退单 `token_env`）
- [x] **Web 管理 UI** — 已有内嵌单页 dashboard(`internal/adminui`, `/ui` + `/ui/capabilities` 能力发现)；本批次新增只读 **Routes 面板**：后端 `/admin/routes-info`(只读, 只暴露 token 的 env 变量名, 绝不泄露 token 值) + 全局 retry 策略 + per-route breaker 状态；OSS 默认 `{status,routes}`, EE 叠加 `{keys,usage,audit}`。写配置/热更新按需再做

## 待澄清 / 观察项
- [x] 日志 `upstream` 字段改为打印解析后 URL —— 抽 `Upstream.ResolvedBaseURL()` 统一 env: 解析，日志与请求共用同一来源

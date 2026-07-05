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
  - [ ] Bedrock SigV4（需 AWS V4 签名新代码：HMAC 密钥派生链 + canonical request + SignedHeaders），独立批次，按需再做
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
- [ ] **③ 通用高熵 secret 检测** — 现规则全靠已知前缀（sk-/AKIA/ghp_）。补 JWT（`eyJ...`）、赋值型泄露（`password=...`/`secret: ...`）、高熵 token（gitleaks 类标配）。

### P1 — 产品成熟度
- [x] **④ 审计查询入口**（`GET /admin/audit?limit=&rule=` + UI Masking 面板） — 脱敏审计只写 `audit.jsonl`，OSS 无查询面。加只读 `GET /admin/audit`（近 N 条 + 按规则过滤，仅元数据）+ UI Audit 面板——让用户看见网关拦了什么（产品价值展示面）。
- [x] **⑤ Claude Code 接入指引 + 部署物料**（Dockerfile + docker-compose + README 中英双语 Claude Code/Docker 章节）
- [x] **⑥ Prometheus 维度细化**（per-route 请求/失败计数 + 延迟直方图 + per-rule PII 命中计数 + injection/transform 拦截计数）
- [ ] **⑦ 配置热加载** — routes 改动需重启，网关类产品应支持 SIGHUP/watch 热载（与「写配置/热更新」同批）。

> 执行顺序：① → ② → ④ → ⑤ → ⑥；③⑦ 按需。

### backlog（按需再做）
- [ ] Bedrock SigV4（AWS V4 签名，见 B 段）
- [x] **健康探针 / 上游可用性探测** — `/health` 被动聚合各路由 breaker 状态：任一非 `closed` 即 `status: degraded`，进程存活始终返回 HTTP 200（body 供 LB/readiness 判读），无主动探测 goroutine（零上游副作用）
- [x] Prometheus `/metrics/prometheus` 导出 — 手写文本格式（零依赖）：请求计数/在途/breaker 状态；维度细化见 D-⑥
- [x] **上游重试** — 全局 `retry`：瞬时失败（网络错误 / 429 / 5xx）线性退避重试（`backoff_ms * attempt`），4xx 立即返回；`max_attempts<=1` 默认关闭
- [x] **多 key 负载均衡** — `upstream.token_envs` 多 key 轮询（原子游标按 route ID 持久化，跳过空值 env，回退单 `token_env`）
- [x] **Web 管理 UI** — 已有内嵌单页 dashboard(`internal/adminui`, `/ui` + `/ui/capabilities` 能力发现)；本批次新增只读 **Routes 面板**：后端 `/admin/routes-info`(只读, 只暴露 token 的 env 变量名, 绝不泄露 token 值) + 全局 retry 策略 + per-route breaker 状态；OSS 默认 `{status,routes}`, EE 叠加 `{keys,usage,audit}`。写配置/热更新按需再做

## 待澄清 / 观察项
- [x] 日志 `upstream` 字段改为打印解析后 URL —— 抽 `Upstream.ResolvedBaseURL()` 统一 env: 解析，日志与请求共用同一来源

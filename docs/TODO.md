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

### backlog（按需再做）
- [ ] Bedrock SigV4（AWS V4 签名，见 B 段）
- [ ] 健康探针 / 上游可用性探测
- [ ] Prometheus `/metrics` 导出
- [ ] 多 key 负载均衡 / 上游重试 / Web 管理 UI

## 待澄清 / 观察项
- [x] 日志 `upstream` 字段改为打印解析后 URL —— 抽 `Upstream.ResolvedBaseURL()` 统一 env: 解析，日志与请求共用同一来源

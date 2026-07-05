# 功能说明

[English](FEATURES.md) | 简体中文

AIGis 能力总览。安装与上手见 [`README.zh-CN.md`](../README.zh-CN.md)；每个配置字段见
[配置指南](CONFIGURATION.zh-CN.md)。

## AIGis 是什么

AIGis 是一个用 Go 编写的 AI 安全网关。它位于客户端（通常是 Claude Code 之类的
agent）与上游 LLM 之间，对客户端讲 OpenAI API 形态，向真实 provider 转发。

它的核心职责是**出站管控（egress control）**：防止请求中的 PII / 敏感数据发到
LLM。命中的敏感数据在请求发往上游前被 tokenize（占位符替换），响应回来时还原，
因此模型永远看不到原文，而客户端仍能得到完整答案。

> 定位：AIGis 管控**发出去什么**（对请求做入站 egress 管控）。输出脱敏
> （“防 LLM 返回什么”）不在范围内，是有意为之。

## 功能地图

| 领域 | 能力 | 默认 |
|------|------|------|
| 路由 | 请求体字段正则路由，首个匹配命中 | — |
| Provider | OpenAI 兼容 + 协议翻译适配 | — |
| 数据保护 | PII / 敏感数据脱敏 + 往返还原 | 按路由启用 |
| 数据保护 | 内置 + 自定义脱敏规则 | 内置启用 |
| 数据保护 | 强出站泄露复检（`force_block`） | 关 |
| 入站防护 | Prompt 注入 / 越狱检测 | 关 |
| 入站防护 | 请求体大小 / token 预算预检 | 关 |
| 可靠性 | per-route 熔断器 | 关 |
| 可靠性 | 上游重试（线性退避） | 关 |
| 可靠性 | 多 key 轮询负载均衡 | 关 |
| 可靠性 | 全局并发限流 | 关（不限） |
| 性能 | 非流式响应缓存（TTL） | 关 |
| 流式 | SSE 透传 + 跨块 unmask + 翻译器 | 开 |
| 可观测 | 审计日志（仅元数据） | 开 |
| 可观测 | `/health` 熔断聚合 | 开 |
| 可观测 | `/metrics` JSON 快照 | 开 |
| 可观测 | 管理面板 UI（`/ui`） | 开 |

## 路由引擎

请求按 `engine.routes` **顺序**匹配，首个命中的路由胜出。路由的 `matcher` 把请求体
JSON 字段（通常是 `model`）映射到一个 Go 正则；空 matcher（`{}`）是 catch-all，必须
放最后。每条路由定义上游（`base_url` + `path`，`env:VAR` 在请求时解析）、认证策略、
可选 header 策略，以及一条有序的 transform 流水线。

完整字段参考见 [配置指南](CONFIGURATION.zh-CN.md)。

## Provider

客户端始终对 AIGis 讲 OpenAI `/chat/completions` 形态。Provider 分两类：

- **OpenAI 兼容**（无协议翻译）—— OpenAI、Gemini（兼容端点）、DeepSeek、
  Qwen/DashScope、Moonshot/Kimi、Azure OpenAI。把 `base_url` 指向厂商端点、匹配
  model 前缀即可新增一条。
- **协议翻译**（请求/响应由 transform 重塑）—— Anthropic Claude 原生
  `/v1/messages` 与 GLM（`x-api-key` + `anthropic-version` + `pii_claude`），以及
  Dify（`/chat-messages`，template 重塑 + `dify` 流式翻译器）。

以上全部的可直接使用路由示例见 [`configs/config.yaml`](../configs/config.yaml)。
端点与 token 环境变量见 [支持的 Provider](../README.zh-CN.md#支持的-provider) 表。

## 数据保护（PII / 敏感数据脱敏）

`pii` transform 在请求离开网关前，把每个命中的敏感数据替换为稳定占位符
（`__AIGIS_SEC_<hash>__`），映射存入 per-request vault，响应时还原——包括跨流式 SSE
分块还原（被拆到多个 delta 的占位符会被重组）。

**内置规则**（越具体越先匹配）：私钥、AWS Access Key、OpenAI API Key、GitHub
Token、Google API Key、邮箱、手机号。

- **按路由选规则** —— `rules` 配置为某条路由挑选规则子集。
- **邮箱模式** —— `full`（整封地址 tokenize，默认）或 `local`（保留 `@domain`
  可见，返回时仍还原完整地址）。
- **自定义规则** —— 加业务专属模式（身份证、订单号……），可全局
  （`security.custom_rules`）或按路由（`custom_rules` 配置，JSON 数组）。非法正则
  启动期响亮报错。

详见 [配置指南 · 脱敏](CONFIGURATION.zh-CN.md)。

### 强出站复检（`force_block`）

设 `force_block: true` 增加发送前的最终泄露复检。流式请求内部降级为 blocking 路径，
以便对完全脱敏后的请求体用**内置规则**再扫一遍；若有内置敏感数据漏网即拒发。客户端
仍收到 SSE 响应（缓冲结果以伪流式重放），感知不到降级。

## 入站防护

两个请求侧 transform，在请求到达上游前拦掉坏请求：

- **Prompt 注入 / 越狱检测**（`injection`）—— 对常见攻击的大小写不敏感启发式规则
  （`ignore previous instructions`、DAN、系统提示词泄露……）。`mode: block`（默认）
  命中即中止请求；`mode: warn` 只记 ctx 元数据。`extra_patterns` 追加路由级正则。
- **大小 / token 预算预检**（`guard`）—— `max_bytes` 拒绝超大请求体；`max_tokens`
  拒绝超预算请求，省掉无谓的上游花费。

## 可靠性

- **熔断器** —— per-route 三态机（closed / open / half-open）。连续失败达
  `fail_threshold` 后，该路由在 `cooldown_sec` 内返回 HTTP 503，之后进入 half-open
  放探针。
- **上游重试** —— 全局 `retry`：瞬时失败（网络错误、429、5xx）线性退避重试
  （`backoff_ms × attempt`）；4xx 立即返回。`max_attempts <= 1` 关闭。
- **多 key 负载均衡** —— `upstream.token_envs` 在多个 key 间轮询（原子游标按 route ID
  持久化，跳过空值 env，回退单 `token_env`）。
- **并发限流** —— `limit.max_concurrent` 限制在途请求数，超出返回 HTTP 429。
  `0` = 不限。

## 性能

- **响应缓存** —— 非流式 `cache`，per-entry TTL + 硬上限 `max_entries`。TTL 内相同
  请求返回上次响应（`X-Cache: HIT`）。内存含明文，TTL 宜短。`0` = 禁用。

## 可观测与运维

- **审计日志** —— 每个发生脱敏的请求向 `./logs/audit.jsonl` 追加一条仅元数据的 JSON
  行（规则类型 + 计数 + 打码预览，**不落明文**）。干净请求不写。
- **`/health`** —— 被动聚合各路由熔断状态：任一非 `closed` 即 `status: degraded`；
  进程存活始终返回 HTTP 200 供 LB / readiness 判读（无主动上游探测）。
- **`/metrics`** —— JSON 快照：in-flight、峰值并发、total、success、failed、uptime。
- **管理面板** —— `/ui` 内嵌单页 UI，通过 `/ui/capabilities` 能力发现自适应构建。
  开源核心构建提供 **Status** 与只读 **Routes** 面板（路由表含上游、transforms、
  token 环境变量**名**——绝不含值——key 数量、熔断状态，以及全局 retry 策略）。
  企业版构建叠加 **Keys / Usage / Audit** 面板。
- **日志** —— 结构化日志同时输出 stdout 和 `./logs/aigis.log`；可选内置滚动切割
  （`log.rotate`，默认关——否则交给系统 `logrotate`）。

## 配置与运维基础

配置优先级：**环境变量（`AIGIS_*`）> 命令行参数 > `config.yaml`**。敏感凭证只通过
配置里按名引用的环境变量提供——绝不硬编码。每个配置段见
[配置指南](CONFIGURATION.zh-CN.md)。

## 授权

开源核心（open-core）：核心采用 **AGPLv3**；企业功能在独立的专有模块中。见
[`README.zh-CN.md`](../README.zh-CN.md#许可证) 与 [`COMMERCIAL.md`](../COMMERCIAL.md)。

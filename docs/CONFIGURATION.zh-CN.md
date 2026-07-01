# 配置指南

[English](CONFIGURATION.md) | 简体中文

本文详解 [`configs/config.yaml`](../configs/config.yaml) 的每个配置节、路由模型，
以及脱敏（PII masking）的工作方式。

配置优先级：**环境变量 (`AIGIS_*`) > 命令行参数 > config.yaml**。

- 默认配置文件：`configs/config.yaml`
- 自定义路径：`./bin/aigis --config /path/to/config.yaml serve`

## 目录

- [顶层配置节](#顶层配置节)
- [路由引擎](#路由引擎)
  - [Matcher（匹配器）](#matcher匹配器)
  - [Upstream（上游）](#upstream上游)
  - [Header policy（请求头策略）](#header-policy请求头策略)
  - [Transforms（转换）](#transforms转换)
- [脱敏（PII masking）](#脱敏pii-masking)
  - [内置规则](#内置规则)
  - [按路由选规则 & email 模式](#按路由选规则--email-模式)
  - [自定义规则](#自定义规则)
- [强审核（force_block）](#强审核force_block)

## 顶层配置节

| 配置节 | 键 | 默认值 | 含义 |
|--------|----|--------|------|
| `server` | `host` / `port` | `0.0.0.0` / `8080` | 监听地址。 |
| `log` | `level` | `debug` | `debug` / `info` / `warn` / `error`。 |
| `log` | `file` | `./logs/aigis.log` | 日志文件路径（同时输出到 stdout）。 |
| `log` | `rotate` | `false` | 内置 lumberjack 滚动。若用系统 `logrotate` 则关闭——切勿两者同管一个文件。 |
| `log` | `max_size_mb` / `max_backups` / `max_age_days` / `compress` | `100` / `7` / `30` / `true` | 仅在 `rotate: true` 时生效。 |
| `audit` | `enabled` | `true` | 每个触发脱敏的请求向 `./logs/audit.jsonl` 追加一行「仅元数据」JSON（规则类型 + 计数 + 占位符，**无明文**）。干净请求不写。 |
| `limit` | `max_concurrent` | `0` | 并发请求上限，超出返回 HTTP 429。`0` = 不限。 |
| `breaker` | `enabled` / `fail_threshold` / `cooldown_sec` | `false` / `5` / `30` | 按路由熔断：上游连续失败 N 次后，该路由在冷却期内返回 HTTP 503，随后半开放探测。 |
| `cache` | `ttl_sec` / `max_entries` | `0` / `1000` | 非流式响应缓存。TTL 内相同请求返回上次响应（`X-Cache: HIT`）。`0` = 关闭。内存中存明文——TTL 要短。 |
| `security` | `custom_rules` | （无） | 全局自定义脱敏规则——见 [自定义规则](#自定义规则)。 |
| `openai` | `api_key` / `base_url` / `model` | — | 遗留单路由兜底，仅在 `engine.routes` 为空时使用。 |
| `engine` | `routes` | — | 路由表（优先级高于 `openai`）。 |

## 路由引擎

请求按 `engine.routes` 的**顺序**匹配，命中第一条即用。最具体的路由放前面，
兜底路由放最后。

```yaml
engine:
  routes:
    - id: "openai-default"
      matcher:
        model: "^gpt-.*"
      upstream:
        base_url: "https://api.openai.com/v1"
        path: "/chat/completions"
        auth_strategy: "bearer"
        token_env: "AIGIS_OPENAI_API_KEY"
      transforms:
        - type: "pii"
          config: {}
```

### Matcher（匹配器）

`matcher` 把请求体 JSON 字段映射到一个 Go 正则。常用键是 `model`。
**空 matcher（`{}`）匹配一切**——只用于最后的兜底路由。

```yaml
matcher:
  model: "^gpt-.*"   # gpt-3.5-turbo, gpt-4, ...
```

### Upstream（上游）

| 字段 | 含义 |
|------|------|
| `base_url` | 上游 base URL。支持 `env:VAR`——请求时从该环境变量解析（日志显示解析后的 URL）。 |
| `path` | 端点路径（默认 `/chat/completions`）。 |
| `auth_strategy` | `bearer`（`Authorization: Bearer <token>`）、`header`（自定义头）、`query`（`api_key` query 参数）或 `none`。 |
| `token_env` | 存放 token 的环境变量名。 |
| `header_name` | `header` 策略下的头名（默认 `Authorization`）。 |

### Header policy（请求头策略）

控制哪些头到达上游。按序应用：**Allow → Set → Remove → Auth**（auth 头总是覆盖）。
适用于用自定义头而非 Bearer 鉴权的 provider（如 Anthropic 的 `x-api-key`）。

```yaml
header_policy:
  allow: ["anthropic-version", "content-type", "anthropic-beta"]
  set:
    "x-api-key": "env:AIGIS_ANTHROPIC_KEY"   # env:VAR 请求时解析
  remove: ["authorization"]
```

### Transforms（转换）

`transforms` 是应用于**请求体**的有序流水线；`response_transforms` 在 unmask 前
应用于（非流式）**响应体**。

| type | 用途 |
|------|------|
| `pii` | 脱敏（tokenize → vault → 返回时 unmask）。 |
| `pii_claude` | 适配 Anthropic `/messages` 形态的脱敏。 |
| `template` | 用 Go text/template 重塑请求体（如 OpenAI → Dify 载荷）。 |
| `field_map` | 字段映射 `目标: 来源`（如 `"prompt": "messages.0.content"`）。 |

流式场景用 `stream_translate` 指定翻译器，把上游 SSE 转回 OpenAI
`chat.completion.chunk` 事件（如 `"dify"`）；空值表示透传 + 跨 chunk unmask。

## 脱敏（PII masking）

`pii` transform 在请求离开网关前，把每个检出的敏感串替换为形如
`__AIGIS_SEC_<hash>__` 的稳定占位符，映射存入 per-request vault，返回时 unmask
还原。同一请求内相同敏感串映射到相同占位符。

### 内置规则

按此顺序应用（更具体的模式在前）：

| 规则名 | 检测 | 替换标签 |
|--------|------|----------|
| `Private Key` | `-----BEGIN ... PRIVATE KEY-----` 块 | `[PRIVATE_KEY_REDACTED]` |
| `AWS Access Key` | `AKIA...`（20 位） | `[AWS_AK_REDACTED]` |
| `OpenAI API Key` | `sk-` / `sk-proj-` 密钥 | `[OPENAI_KEY_REDACTED]` |
| `GitHub Token` | `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_` token | `[GITHUB_TOKEN_REDACTED]` |
| `Google API Key` | `AIza...`（39 位） | `[GOOGLE_KEY_REDACTED]` |
| `Email` | 邮箱地址 | `[EMAIL_REDACTED]` |
| `Mobile Phone` | 中国手机号（11 位，可选 `+86`） | `[PHONE_REDACTED]` |

### 按路由选规则 & email 模式

`pii` transform 的 `config` 支持：

- `rules` —— 逗号分隔的规则名，只在**本路由**应用（如 `"Email,Mobile Phone"`）。
  省略/空 = 全部规则。名字为上述内置名加任意自定义规则名。
- `email` —— 邮箱脱敏模式：
  - `full`（默认、省略）—— 整个地址脱敏（最严格）。
  - `local` —— 只脱敏邮箱名，保留 `@domain` 给模型看。返回时仍还原完整地址。

```yaml
transforms:
  - type: "pii"
    config:
      rules: "Email,Mobile Phone"
      email: "local"
```

### 自定义规则

添加业务专属模式（身份证、订单号等）。每条规则的 `pattern` 是 Go 正则；
非法正则在**启动时报错**（fail loud）。名为 `"Order ID"` 的规则生成标签
`[ORDER_ID_REDACTED]`。

**全局**（对所有路由生效）—— 在 `security` 下：

```yaml
security:
  custom_rules:
    - name: "ID Card"
      pattern: '\b\d{17}[\dXx]\b'
    - name: "Bank Card"
      pattern: '\b\d{16,19}\b'
```

**按路由**—— 通过 `pii` transform 的 `custom_rules` 键。由于 transform 的
`config` 值是字符串，这里的值是一个 `{name, pattern}` 的 **JSON 数组字符串**
（进程级编译缓存，不污染共享 scanner）：

```yaml
transforms:
  - type: "pii"
    config:
      custom_rules: '[{"name":"Order ID","pattern":"ORD-\\d{8}"}]'
```

## 强审核（force_block）

在路由上设 `force_block: true` 启用发送前泄露复检。流式请求（`stream: true`）
内部降级为 blocking 路径，脱敏后的完整请求体在发往上游前用**内置**规则再扫一遍；
若有内置敏感串漏过脱敏，请求被拒发。客户端仍收到 SSE 响应（缓冲结果以伪流式
重放），感知不到降级。

> 注意：只有内置规则拦截 egress——按路由的自定义规则标记的是业务 ID（订单号等），
> 不是泄露级密钥，其残留不会拦截请求。

```yaml
- id: "strict-route"
  matcher:
    model: "^gpt-.*"
  force_block: true
  upstream:
    base_url: "https://api.openai.com/v1"
    path: "/chat/completions"
    auth_strategy: "bearer"
    token_env: "AIGIS_OPENAI_API_KEY"
  transforms:
    - type: "pii"
      config: {}
```

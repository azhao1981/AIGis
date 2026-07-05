# AIGis

[English](README.md) | 简体中文

## AIGis 是什么

AIGis 是一个用 Go 语言开发的 AI 安全网关,为 AI/LLM 服务提供访问控制和数据处理。

完整功能总览见 [功能说明](docs/FEATURES.zh-CN.md)。

## 安装

### 下载预编译二进制

从 [最新 release](https://github.com/azhao1981/AIGis/releases/latest) 下载对应平台的压缩包:

```bash
# 示例: Linux amd64
VERSION=v0.1.0
curl -fsSL -o aigis.tar.gz \
  "https://github.com/azhao1981/AIGis/releases/download/${VERSION}/aigis_${VERSION}_linux_amd64.tar.gz"

# 校验 checksum (可选, 推荐)
curl -fsSL -O "https://github.com/azhao1981/AIGis/releases/download/${VERSION}/SHA256SUMS"
sha256sum -c SHA256SUMS --ignore-missing

tar -xzf aigis.tar.gz
./aigis version
```

可用压缩包: `linux_amd64`、`linux_arm64`、`darwin_amd64`、`darwin_arm64`、`windows_amd64` (`.zip`)。

### 从源码构建

需要 Go 1.25 (1.23 / 1.24 亦可):

```bash
git clone https://github.com/azhao1981/AIGis.git
cd AIGis
make build      # 生成 ./bin/aigis
./bin/aigis version
```

## 支持的 Provider

客户端始终以 **OpenAI `/chat/completions`** 格式访问 AIGis;网关脱敏后转发给上游。Provider 分两类:

**OpenAI 兼容** — 无需协议翻译,把 `base_url` 指向厂商端点、匹配 model 前缀即可新增路由:

| Provider | Base URL | Model 前缀 | Token 环境变量 |
|----------|----------|-----------|----------------|
| OpenAI | `https://api.openai.com/v1` | `gpt-*` | `OPENAI_API_KEY` |
| Gemini(经兼容端点) | `env:AIGIS_GEMINI_BASE_URL` | `gemini-*` | `GEMINI_API_KEY` |
| DeepSeek | `https://api.deepseek.com/v1` | `deepseek-*` | `DEEPSEEK_API_KEY` |
| 通义千问(DashScope) | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `qwen*` | `DASHSCOPE_API_KEY` |
| Moonshot / Kimi | `https://api.moonshot.ai/v1` | `moonshot*` / `kimi*` | `MOONSHOT_API_KEY` |
| Azure OpenAI | `https://<resource>.openai.azure.com` | `azure-*` | `AIGIS_AZURE_KEY` |

Azure 仍是 OpenAI 形态,但两点不同,均纯配置搞定:`api-version` 走 query(拼进 `path`),鉴权用 `api-key` 头(`auth_strategy: header`、`header_name: api-key`)而非 Bearer。deployment 名放进 path,如 `/openai/deployments/gpt-4o/chat/completions?api-version=2024-12-01-preview`。

**协议翻译** — 请求/响应经 transform 重塑:

| Provider | Path | 说明 |
|----------|------|------|
| Anthropic Claude(原生 `/v1/messages`)/ GLM | `/messages` | `x-api-key` 头鉴权 + `anthropic-version` + `pii_claude` transform |
| Dify | `/chat-messages` | 模板重塑 + `dify` 流式翻译器 |

以上路由的可用示例均在 [`configs/config.yaml`](configs/config.yaml) 中(OpenAI 兼容那几个默认注释掉了,取消注释并设置对应 `*_API_KEY` 即可)。

## 快速上手

启动网关后，以 OpenAI 格式向它发请求即可。prompt 里的敏感数据（邮箱、手机号、
API Key 等）在请求离开网关前会被脱敏，返回时再还原：

```bash
# 1. 为默认 gpt-* 路由设置上游 Key 并启动网关
export AIGIS_OPENAI_API_KEY=sk-your-real-key
./bin/aigis serve

# 2. 另开一个终端，像调用 OpenAI API 一样调用它
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "我的邮箱是 alice@example.com，手机号是 13800138000，帮我拟一封回信。"}
    ]
  }'
```

上游实际收到的是脱敏后的内容：`我的邮箱是 [EMAIL_REDACTED]，手机号是
[PHONE_REDACTED]，...` —— 占位符会在返回给客户端的回复里还原成原值。模型全程
看不到明文 PII，而客户端仍能得到连贯的回答。

每次触发脱敏的请求都会向 `./logs/audit.jsonl` 追加一行「仅元数据」记录（规则 +
计数，无明文）。可在 `/ui`（Masking 面板）或 `GET /admin/audit?limit=50&rule=Email`
查看。

## 接入 Claude Code

AIGis 同时暴露 Anthropic 原生 `/v1/messages` 端点，Claude Code 这类 agent 只需
一个环境变量即可走网关——每条 prompt 在离开本机前都会被扫描/脱敏（邮箱、手机号、
API Key、私钥整块等）：

```bash
# 1. 把 claude-proxy 路由指向你的 Anthropic 兼容上游
export AIGIS_ANTHROPIC_BASE_URL=https://api.anthropic.com/v1
export AIGIS_ANTHROPIC_KEY=sk-ant-your-real-key
./bin/aigis serve

# 2. 让 Claude Code 走网关
export ANTHROPIC_BASE_URL=http://localhost:8080
claude
```

内置的 `claude-proxy` 路由（见 [`configs/config.yaml`](configs/config.yaml)）匹配
`claude*`/`glm*` 模型，向上游注入 `x-api-key`，并应用 `pii_claude` 脱敏转换。占位
符在流式响应中还原，agent 正常工作，模型全程看不到明文敏感信息。

## Docker 运行

```bash
# .env 存放上游 Key（AIGIS_OPENAI_API_KEY=...、AIGIS_ANTHROPIC_KEY=... 等）
docker compose up -d --build

# 或直接 docker
docker build -t aigis .
docker run -d --name aigis -p 8080:8080 --env-file .env -v "$PWD/logs:/app/logs" aigis
```

镜像以非 root 用户运行，暴露 `:8080`，健康检查走 `/health`，通过卷持久化
`./logs`（网关日志 + 仅元数据的 `audit.jsonl`）。如需自定义路由，挂载自己的
`configs/config.yaml`（见 [`docker-compose.yml`](docker-compose.yml) 中的注释行）。

## 使用方式

### 启动服务 (默认 0.0.0.0:8080)

```bash
./bin/aigis serve
```

### 指定端口

```bash
./bin/aigis serve -p 3000
```

### 使用环境变量

```bash
AIGIS_SERVER_PORT=9000 ./bin/aigis serve
```

### 使用自定义配置文件

```bash
./bin/aigis --config /path/to/config.yaml serve
```

配置优先级:

```
环境变量 (AIGIS_*) > 命令行参数 > config.yaml
```

每个配置节、路由模型以及脱敏工作方式，详见
[配置指南](docs/CONFIGURATION.zh-CN.md)。

### 热加载 routes 与自定义规则（SIGHUP）

向运行中的网关发 `SIGHUP` 会重新读取 `engine.routes` 与 `security.custom_rules`
并原子替换 —— 无需重启、不丢请求。坏配置会 fail loud（记一条 error 日志）但
**不影响在线状态**，所以编辑配置文件永远不会搞挂正在运行的网关：

```bash
kill -HUP $(pidof aigis)
# 或在 docker 下：
docker exec aigis kill -HUP 1
```

其他配置项（log/limit/breaker/cache/retry/audit）仍需重启，故意保持 reload 范围
小而可控。

## 项目结构

```bash
  aigis/
  ├── bin/aigis               # 编译产物
  ├── cmd/aigis/              # 入口 + Cobra/Viper 命令行 (serve 子命令)
  ├── internal/
  │   ├── core/
  │   │   ├── context.go       # AIGisContext (线程安全 metadata + 脱敏 vault)
  │   │   ├── provider.go      # Provider 接口 (LLM 适配器)
  │   │   ├── providers/       # UniversalProvider (配置驱动适配器)
  │   │   ├── engine/          # 路由匹配 + 配置校验
  │   │   ├── transform/       # pii / injection / guard / template / 流式翻译器
  │   │   ├── security/        # 敏感数据扫描器 (内置 + 自定义规则)
  │   │   ├── breaker/         # per-route 熔断器
  │   │   ├── limiter/         # 全局并发限流
  │   │   ├── cache/           # 非流式响应缓存
  │   │   ├── metrics/         # in-flight / success / failed 计数
  │   │   └── audit/           # 仅元数据的脱敏审计日志
  │   ├── server/              # HTTP 服务器、网关 handler、中间件链
  │   ├── adminui/             # 内嵌管理面板 (/ui + capabilities)
  │   ├── config/              # 配置加载 (env / flags / config.yaml)
  │   └── pkg/logger/          # 结构化日志 (+ 可选滚动切割)
  ├── configs/
  │   └── config.yaml          # 默认配置
  ├── go.mod
  └── go.sum
```

## 测试

```bash
go test -v ./tests/...
```

## 许可 (License)

本项目采用 **GNU AGPLv3** 协议开源,详见 [`LICENSE`](LICENSE)。

个人、研究及内部评估可在 AGPLv3 条款下免费使用。若需在闭源 / SaaS 等场景中使用且不愿承担 AGPLv3 的源码开放义务,请参阅 [`COMMERCIAL.md`](COMMERCIAL.md) 获取商业授权。

向本项目贡献代码前,请阅读并同意 [`CLA.md`](CLA.md)。

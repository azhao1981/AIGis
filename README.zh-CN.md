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
计数，无明文）。

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

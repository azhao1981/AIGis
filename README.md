# AIGis

English | [简体中文](README.zh-CN.md)

## What is AIGis

AIGis is an AI security gateway written in Go that provides access control and data processing for AI/LLM services.

## Supported Providers

Clients always speak the **OpenAI `/chat/completions`** shape to AIGis; the gateway masks PII and forwards to the upstream. Providers fall into two groups:

**OpenAI-compatible** — no protocol translation, add a route by pointing `base_url` at the vendor endpoint and matching the model prefix:

| Provider | Base URL | Model prefix | Token env |
|----------|----------|--------------|-----------|
| OpenAI | `https://api.openai.com/v1` | `gpt-*` | `OPENAI_API_KEY` |
| Gemini (via compatible endpoint) | `env:AIGIS_GEMINI_BASE_URL` | `gemini-*` | `GEMINI_API_KEY` |
| DeepSeek | `https://api.deepseek.com/v1` | `deepseek-*` | `DEEPSEEK_API_KEY` |
| Qwen (DashScope) | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `qwen*` | `DASHSCOPE_API_KEY` |
| Moonshot / Kimi | `https://api.moonshot.ai/v1` | `moonshot*` / `kimi*` | `MOONSHOT_API_KEY` |

**Protocol-translated** — request/response are reshaped by transforms:

| Provider | Path | Notes |
|----------|------|-------|
| Anthropic Claude / GLM | `/messages` | `x-api-key` header auth + `pii_claude` transform |
| Dify | `/chat-messages` | template reshaping + `dify` stream translator |

Ready-to-use route examples for all of the above are in [`configs/config.yaml`](configs/config.yaml) (the OpenAI-compatible ones are commented out — uncomment and set the matching `*_API_KEY`).

## Usage

### Start the server (default 0.0.0.0:8080)

```bash
./bin/aigis serve
```

### Specify a port

```bash
./bin/aigis serve -p 3000
```

### Use environment variables

```bash
AIGIS_SERVER_PORT=9000 ./bin/aigis serve
```

### Use a custom config file

```bash
./bin/aigis --config /path/to/config.yaml serve
```

Configuration precedence:

```
environment variables (AIGIS_*) > command-line flags > config.yaml
```

## Project Structure

```bash
  aigis/
  ├── bin/aigis               # build artifact
  ├── cmd/aigis/
  │   ├── main.go              # entry point
  │   ├── root.go              # Cobra root command + Viper config
  │   └── serve.go             # serve subcommand
  ├── internal/
  │   ├── core/
  │   │   ├── context.go       # GatewayContext (thread-safe metadata)
  │   │   ├── provider.go      # Provider interface (LLM adapter)
  │   │   └── processor.go     # Processor interface (middleware)
  │   └── server/
  │       └── server.go        # HTTP server (graceful shutdown)
  ├── configs/
  │   └── config.yaml          # default config
  ├── go.mod
  └── go.sum
```

## Test

```bash
go test -v ./tests/...
```

## License

This project is open-sourced under the **GNU AGPLv3** license, see [`LICENSE`](LICENSE).

Personal, research, and internal-evaluation use is free under the AGPLv3 terms. If you intend to use AIGis in closed-source / SaaS scenarios without complying with the AGPLv3 source-disclosure obligations, please see [`COMMERCIAL.md`](COMMERCIAL.md) for a commercial license.

Before contributing, please read and agree to the [`CLA.md`](CLA.md).

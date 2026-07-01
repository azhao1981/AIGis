# AIGis

English | [简体中文](README.zh-CN.md)

## What is AIGis

AIGis is an AI security gateway written in Go that provides access control and data processing for AI/LLM services.

## Install

### Download a pre-built binary

Grab the archive for your platform from the [latest release](https://github.com/azhao1981/AIGis/releases/latest):

```bash
# Example: Linux amd64
VERSION=v0.1.0
curl -fsSL -o aigis.tar.gz \
  "https://github.com/azhao1981/AIGis/releases/download/${VERSION}/aigis_${VERSION}_linux_amd64.tar.gz"

# Verify the checksum (optional but recommended)
curl -fsSL -O "https://github.com/azhao1981/AIGis/releases/download/${VERSION}/SHA256SUMS"
sha256sum -c SHA256SUMS --ignore-missing

tar -xzf aigis.tar.gz
./aigis version
```

Available archives: `linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64`, `windows_amd64` (`.zip`).

### Build from source

Requires Go 1.25 (1.23 / 1.24 also supported):

```bash
git clone https://github.com/azhao1981/AIGis.git
cd AIGis
make build      # produces ./bin/aigis
./bin/aigis version
```

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

## Quick Start

Start the gateway, then send an OpenAI-style request to it. Sensitive data in the
prompt (email, phone, API keys, ...) is masked before the request leaves the
gateway, and restored in the response on the way back:

```bash
# 1. Set the upstream key for the default gpt-* route and start the gateway
export AIGIS_OPENAI_API_KEY=sk-your-real-key
./bin/aigis serve

# 2. In another terminal, call it like the OpenAI API
curl -s http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {"role": "user", "content": "My email is alice@example.com and my phone is 13800138000. Help me draft a reply."}
    ]
  }'
```

What the upstream actually receives (masked): `My email is [EMAIL_REDACTED] and
my phone is [PHONE_REDACTED]. ...` — the placeholders are un-masked back to the
original values in the reply returned to the client, so the model never sees the
raw PII but your client still gets a coherent answer.

Every masked request appends one metadata-only line (rule + counts, no
plaintext) to `./logs/audit.jsonl`.

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

For every config section, the routing model, and how masking works, see the
[Configuration Guide](docs/CONFIGURATION.md).

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

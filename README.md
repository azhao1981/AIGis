# AIGis

English | [简体中文](README.zh-CN.md)

## What is AIGis

AIGis is an AI security gateway written in Go that provides access control and data processing for AI/LLM services.

For a complete overview of what AIGis does, see the [Features](docs/FEATURES.md) doc.

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
| Azure OpenAI | `https://<resource>.openai.azure.com` | `azure-*` | `AIGIS_AZURE_KEY` |

Azure still speaks the OpenAI shape but differs in two ways, both config-only: `api-version` goes in the query (append it to `path`), and auth uses the `api-key` header (`auth_strategy: header`, `header_name: api-key`) instead of Bearer. Put your deployment name in the path, e.g. `/openai/deployments/gpt-4o/chat/completions?api-version=2024-12-01-preview`.

**Protocol-translated** — request/response are reshaped by transforms:

| Provider | Path | Notes |
|----------|------|-------|
| Anthropic Claude (native `/v1/messages`) / GLM | `/messages` | `x-api-key` header auth + `anthropic-version` + `pii_claude` transform |
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
plaintext) to `./logs/audit.jsonl`. You can browse it at `/ui` (Masking panel)
or via `GET /admin/audit?limit=50&rule=Email`.

## Use with Claude Code

AIGis also exposes the Anthropic-native `/v1/messages` endpoint, so agents like
Claude Code can go through the gateway with a single env var — every prompt is
scanned/masked (emails, phones, API keys, private key blocks, ...) before it
leaves your machine:

```bash
# 1. Point the claude-proxy route at your Anthropic-compatible upstream
export AIGIS_ANTHROPIC_BASE_URL=https://api.anthropic.com/v1
export AIGIS_ANTHROPIC_KEY=sk-ant-your-real-key
./bin/aigis serve

# 2. Route Claude Code through the gateway
export ANTHROPIC_BASE_URL=http://localhost:8080
claude
```

The bundled `claude-proxy` route (see [`configs/config.yaml`](configs/config.yaml))
matches `claude*`/`glm*` models, injects `x-api-key` upstream, and applies the
`pii_claude` masking transform. Placeholders are restored in the streamed
response, so the agent works normally while the model never sees the raw
secrets.

## Run with Docker

```bash
# .env holds the upstream keys (AIGIS_OPENAI_API_KEY=..., AIGIS_ANTHROPIC_KEY=..., ...)
docker compose up -d --build

# or plain docker
docker build -t aigis .
docker run -d --name aigis -p 8080:8080 --env-file .env -v "$PWD/logs:/app/logs" aigis
```

The image runs as a non-root user, exposes `:8080`, health-checks `/health`,
and persists `./logs` (gateway log + metadata-only `audit.jsonl`) via the
volume. Mount your own `configs/config.yaml` to customize routes (see the
commented line in [`docker-compose.yml`](docker-compose.yml)).

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
  ├── cmd/aigis/              # entry point + Cobra/Viper CLI (serve subcommand)
  ├── internal/
  │   ├── core/
  │   │   ├── context.go       # AIGisContext (thread-safe metadata + secret vault)
  │   │   ├── provider.go      # Provider interface (LLM adapter)
  │   │   ├── providers/       # UniversalProvider (config-driven adapter)
  │   │   ├── engine/          # route matching + config validation
  │   │   ├── transform/       # pii / injection / guard / template / stream translators
  │   │   ├── security/        # secret scanner (built-in + custom rules)
  │   │   ├── breaker/         # per-route circuit breaker
  │   │   ├── limiter/         # global concurrency limit
  │   │   ├── cache/           # non-streaming response cache
  │   │   ├── metrics/         # in-flight / success / failed counters
  │   │   └── audit/           # metadata-only masking audit log
  │   ├── server/              # HTTP server, gateway handler, middleware chain
  │   ├── adminui/             # embedded admin dashboard (/ui + capabilities)
  │   ├── config/              # config loading (env / flags / config.yaml)
  │   └── pkg/logger/          # structured logging (+ optional rotation)
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

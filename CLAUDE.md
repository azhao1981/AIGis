# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

AIGis 是一个用 Go 开发的 AI 安全网关。客户端统一以 OpenAI `/chat/completions` 协议接入，网关在转发前做 **PII 脱敏 / prompt 注入检测 / 容量预算预检**，按请求体字段匹配路由后转发到上游 LLM，并在响应回程还原脱敏占位符。支持 OpenAI / Gemini / DeepSeek / Qwen / Moonshot / Azure / Anthropic / Dify 等上游（见 [`README.md`](README.md)）。

## 核心架构

### 请求生命周期（必读）

请求从 `HTTPServer.handleGateway`（`internal/server/http.go:217,277`）进入，依次：

1. **入口** — `mux.HandleFunc` 注册 `/v1/chat/completions` 和 `/v1/messages` 都走 `handleGateway`；EE 版通过 `server.Use()`（`internal/server/middleware.go`）在 mux 外层包裹 auth / multi-tenant 中间件。
2. **预检** — 全局并发限流（`core/limiter`，超限 429）→ 客户端 prompt 注入检测（`transform/injection.go`）→ 请求体/token 预算检查。
3. **路由匹配** — `engine.Engine.FindRoute`（`core/engine/router.go:42`）按 `routes` 列表顺序，用**预编译正则**对请求体的 JSON path 取值匹配，命中第一个即停止。配置在启动期由 `engine.Validate` 强校验，坏配置会 fail loud。
4. **脱敏** — `transform` 策略链（`pii` / `field_map` / `template` / `unmask` / `pii_claude` 等）改写请求体；命中规则的原值进 `AIGisContext` 的 vault，占位符（如 `[EMAIL_REDACTED]`）发给上游。每次脱敏向 `./logs/audit.jsonl` 追加一条**仅元数据**（规则+计数，无明文）。
5. **可靠性** — per-route 断路器（`core/breaker`，连续失败超阈值开路返回 503）→ 重试（`provider.RetryPolicy`，仅对 429/5xx/网络错误线性退避）→ 多 key 轮询。
6. **转发** — `UniversalProvider`（`core/providers/universal.go`）按 route 配置（`base_url` / `path` / `auth_strategy` / `header_name`）发上游请求；流式与非流式分别处理。响应缓存（`core/cache`，仅非流式）按命中返回 `X-Cache: HIT`。
7. **回程还原** — `StreamTransformer`（如 `dify_stream.go`）翻译流式片段；`unmask` 策略把占位符还原为 vault 中的原值。EE 版还会经 `usage.Sink` 记一条用量事件。

> 旧的 `Pipeline`/`Processor` 中间件层已移除（曾只用于日志、从不改 body）。请求日志现在内联在 `handleGateway` 里 + `defer` 收尾，转换统一走 Transform 策略。

### 模块边界

- **开源核心** (`internal/`)：`core/context.go`（`AIGisContext`，线程安全 metadata + 脱敏 vault）、`core/provider.go`（Provider 接口，唯一实现 `UniversalProvider`）、`core/engine/`（路由 + 启动期校验）、`core/transform/`（所有真正转换逻辑 + 流式翻译）、`core/security/`（内置+自定义敏感信息扫描）、`core/{breaker,limiter,cache,metrics,audit}`、`server/`（HTTP 入口 + gateway handler + middleware chain）、`adminui/`（内嵌只读面板 `/ui`）、`config/`、`pkg/logger/`。
- **企业版** (`ee/` + `cmd/aigis-ee/`)：在开源核心基础上，通过 `server.Use()` 注入 auth (`ee/auth`)、quota (`ee/quota`)、billing (`ee/billing`)、adminui capabilities (`ee/adminui`) 等中间件。受 `ee/LICENSE` 约束，**不归 AGPLv3**，构建需 `make build-ee`。
- **构建目标**：`make build` → `./bin/aigis`（开源）；`make build-ee` → `./bin/aigis-ee`（企业版，含 ee/）。

### 环境变量约定

上游 key 全部走环境变量，前缀 `AIGIS_`，如 `AIGIS_OPENAI_API_KEY` / `AIGIS_GEMINI_BASE_URL` / `AIGIS_AZURE_KEY`。配置在 route 的 `upstream.api_key` 用 `env:VAR_NAME` 引用。

## 常用命令

### 构建与运行

```bash
make build            # 开源版 → ./bin/aigis
make build-ee         # 企业版 → ./bin/aigis-ee（需要 ee/ 授权）
make run              # build + 运行（默认 :8080）
make run-port PORT=3000
make kill             # 停止运行中的 aigis serve
./run.sh              # 快速重启（先 kill 再 start）

# 自定义配置 / 端口
./bin/aigis --config /path/to/config.yaml serve
AIGIS_SERVER_PORT=9000 ./bin/aigis serve
./bin/aigis serve -p 3000
```

配置优先级：**环境变量 (AIGIS_*) > 命令行参数 > `configs/config.yaml`**。

### 测试（按需跑单文件，不要全量）

```bash
# 单个测试文件 / 单个包（默认推荐）
go test -v ./internal/core/transform/...
go test -v -run TestPII ./internal/core/transform/

# 集成测试（需起上游 mock）
go test -v ./tests/...

# 覆盖率（仅在用户明确要求时跑全量）
make test-coverage    # 输出 coverage.html

# BASH 集成脚本（在 tests/ 和 tmp/run_*.sh 下，需先 make run）
./tests/test_base.sh
./tmp/run_guard_injection_e2e.sh
```

### 代码质量

```bash
make fmt              # gofmt -s -w .
make lint             # go vet ./...（golint 已默认跳过）
```

PR 前检查（见 [`CONTRIBUTING.md`](CONTRIBUTING.md)）：`go build ./...` + `go vet ./...` + `go test ./...` 全绿，且 `make fmt` 已执行。

### 依赖

```bash
make deps             # go mod download + tidy
make update-deps      # go get -u ./... + tidy
```

## Go 版本

推荐 Go 1.25（支持 1.23/1.24/1.25）。当前固定 1.25.4（见 [`.go-version`](.go-version)）。WSL 环境若 toolchain 不匹配，可加 `GOTOOLCHAIN=local` 前缀强制使用本地工具链。

## 关键约定（避免踩坑）

- **测试隔离**：测试脚本和上游 mock 全部写在 `tmp/` 下（如 `tmp/echo_upstream.py`、`tmp/run_*_e2e.sh`），日志输出到 `tmp/logs/`。不要让测试污染生产日志路径 `./logs/`。
- **审计日志不可写明文**：`./logs/audit.jsonl` 只允许写规则类型 + 计数 + 占位符，**严禁出现 PII 原值**。改 `core/audit` 时务必保持。
- **路由顺序敏感**：`engine.FindRoute` 返回第一个全部 matcher 命中的 route。新增 route 时注意在 `configs/config.yaml` 的 `engine.routes` 列表里的位置。
- **Transform 是唯一改 body 的地方**：不要在中间件或 handler 里直接改请求/响应体，新增转换能力请实现 `transform.Strategy` 或 `transform.StreamTranslator` 接口并注册到 `KnownTypes()` / `KnownStreamTranslators()`。
- **EE 隔离**：开源 `internal/` 不能 import `ee/`。EE 通过接口注入（如 `server.SetUsageSink` / `server.SetQuotaLimiter` / `server.Use`），开源版用 NopSink / AllowAll 默认实现。
- **提交规范**：commit message 前缀 `feat:` / `fix:` / `docs:` / `ci:` / `refactor:`（参考 `git log`）。
- **License 边界**：开源代码 AGPLv3，`ee/` 受 `ee/LICENSE` 约束。贡献需同意 [`CLA.md`](CLA.md)。

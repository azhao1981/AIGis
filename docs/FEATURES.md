# Features

English | [简体中文](FEATURES.zh-CN.md)

A complete overview of what AIGis does. For install and getting started see
[`README.md`](../README.md); for every config field see the
[Configuration Guide](CONFIGURATION.md).

## What AIGis is

AIGis is an AI security gateway written in Go. It sits between your client
(typically an agent such as Claude Code) and the upstream LLM, speaking the
OpenAI API shape to the client and forwarding to the real provider.

Its primary job is **egress control**: keep PII / secrets in the request from
ever leaving your boundary to the LLM. Detected secrets are tokenized before the
request goes upstream and restored in the response on the way back, so the model
never sees the raw data while the client still gets a coherent answer.

> Positioning: AIGis guards **what goes out** (inbound egress control on the
> request). Output-side masking ("what the LLM returns") is intentionally
> out of scope.

## Feature map

| Area | Capability | Default |
|------|-----------|---------|
| Routing | Regex body-field routing, first-match-wins | — |
| Providers | OpenAI-compatible + protocol-translated adapters | — |
| Data protection | PII / secret masking with round-trip unmask | on (per route) |
| Data protection | Built-in + custom masking rules | built-in on |
| Data protection | Strict egress leak review (`force_block`) | off |
| Inbound guard | Prompt-injection / jailbreak detection | off |
| Inbound guard | Request size / token budget pre-check | off |
| Reliability | Per-route circuit breaker | off |
| Reliability | Upstream retry (linear backoff) | off |
| Reliability | Multi-key round-robin load balancing | off |
| Reliability | Global concurrency limit | off (unlimited) |
| Performance | Non-streaming response cache (TTL) | off |
| Streaming | SSE passthrough + cross-chunk unmask + translators | on |
| Observability | Audit log (metadata only) | on |
| Observability | `/health` breaker aggregation | on |
| Observability | `/metrics` JSON snapshot | on |
| Observability | Admin dashboard UI (`/ui`) | on |

## Routing engine

Requests are matched against `engine.routes` **in order**; the first matching
route wins. A route's `matcher` maps a request-body JSON field (commonly
`model`) to a Go regexp; an empty matcher (`{}`) is the catch-all and must be
last. Each route defines its upstream (`base_url` + `path`, with `env:VAR`
resolved at request time), an auth strategy, an optional header policy, and an
ordered transform pipeline.

See [Routing engine](CONFIGURATION.md#routing-engine) for the full field
reference.

## Providers

Clients always speak the OpenAI `/chat/completions` shape to AIGis. Providers
fall into two groups:

- **OpenAI-compatible** (no protocol translation) — OpenAI, Gemini (compatible
  endpoint), DeepSeek, Qwen/DashScope, Moonshot/Kimi, Azure OpenAI. Add one by
  pointing `base_url` at the vendor and matching the model prefix.
- **Protocol-translated** (request/response reshaped by transforms) — Anthropic
  Claude native `/v1/messages` and GLM (`x-api-key` + `anthropic-version` +
  `pii_claude`), and Dify (`/chat-messages`, template reshaping + `dify` stream
  translator).

Ready-to-use route examples for all of these are in
[`configs/config.yaml`](../configs/config.yaml). See the
[Supported Providers](../README.md#supported-providers) table for endpoints and
token env vars.

## Data protection (PII / secret masking)

The `pii` transform replaces each detected secret with a stable placeholder
(`__AIGIS_SEC_<hash>__`) before the request leaves the gateway, stores the
mapping in a per-request vault, and un-masks it in the response — including
across streaming SSE chunks (placeholders split across deltas are reassembled).

**Built-in rules** (applied most-specific first): Private Key, AWS Access Key,
OpenAI API Key, GitHub Token, Google API Key, Email, Mobile Phone.

- **Per-route rule selection** — `rules` config picks a subset for a route.
- **Email mode** — `full` (tokenize the whole address, default) or `local`
  (keep `@domain` visible, still restore the full address on return).
- **Custom rules** — add domain-specific patterns (ID cards, order numbers, …)
  either globally (`security.custom_rules`) or per route (`custom_rules` config,
  a JSON array). Invalid regexes fail loud at startup.

See [Sensitive-info masking](CONFIGURATION.md#sensitive-info-masking-pii).

### Strict egress review (`force_block`)

Set `force_block: true` to add a final pre-send leak check. A streaming request
is served internally via the blocking path so the fully-masked body can be
re-scanned with the **built-in** rules; if any built-in secret survived masking,
the request is rejected. The client still receives an SSE response (the buffered
result is re-emitted as a pseudo-stream), so it is unaware of the downgrade.

## Inbound guards

Two request-side transforms reject bad requests before they reach the upstream:

- **Prompt-injection / jailbreak detection** (`injection`) — case-insensitive
  heuristics for common attacks (`ignore previous instructions`, DAN,
  system-prompt leaks, …). `mode: block` (default) aborts the request on a hit;
  `mode: warn` only records it in context metadata. `extra_patterns` adds
  route-scoped regexes.
- **Size / token budget pre-check** (`guard`) — `max_bytes` rejects an oversized
  body; `max_tokens` rejects an over-budget request, saving needless upstream
  cost.

## Reliability

- **Circuit breaker** — per-route three-state breaker (closed / open /
  half-open). After `fail_threshold` consecutive upstream failures the route
  returns HTTP 503 for `cooldown_sec`, then probes half-open.
- **Upstream retry** — global `retry`: transient failures (network error, 429,
  5xx) are retried with linear backoff (`backoff_ms × attempt`); 4xx returns
  immediately. `max_attempts <= 1` disables it.
- **Multi-key load balancing** — `upstream.token_envs` round-robins across
  multiple keys (atomic cursor kept per route ID, empty-valued envs skipped,
  falls back to the single `token_env`).
- **Concurrency limit** — `limit.max_concurrent` caps in-flight requests; excess
  get HTTP 429. `0` = unlimited.

## Performance

- **Response cache** — non-streaming `cache` with per-entry TTL and a hard
  `max_entries`. Identical requests within TTL return the prior response
  (`X-Cache: HIT`). Holds plaintext in memory — keep TTL short. `0` = disabled.

## Observability & operations

- **Audit log** — each masked request appends one metadata-only JSON line to
  `./logs/audit.jsonl` (rule type + counts + masked previews, **no plaintext**).
  Clean requests write nothing.
- **`/health`** — passively aggregates per-route breaker state: any route not
  `closed` yields `status: degraded`; the process always returns HTTP 200 for
  LB / readiness probes (no active upstream probing).
- **`/metrics`** — JSON snapshot: in-flight, peak concurrency, total, success,
  failed, uptime.
- **Admin dashboard** — an embedded single-page UI at `/ui` that adapts to the
  build via `/ui/capabilities` discovery. The open-core build ships the
  **Status** and read-only **Routes** panels (route table with upstream,
  transforms, token env **names** — never values — key count, breaker state, and
  the global retry policy). Enterprise builds add **Keys / Usage / Audit**
  panels.
- **Logging** — structured logs to stdout and `./logs/aigis.log`; optional
  built-in rotation (`log.rotate`, off by default — use system `logrotate`
  otherwise).

## Configuration & operations basics

Configuration precedence: **environment variables (`AIGIS_*`) > command-line
flags > `config.yaml`**. Secrets are supplied only through environment variables
referenced by name in config — never hard-coded. See the
[Configuration Guide](CONFIGURATION.md) for every section.

## Licensing

Open-core: the core is under **AGPLv3**; enterprise features live in a separate
proprietary module. See [`README.md`](../README.md#license) and
[`COMMERCIAL.md`](../COMMERCIAL.md).

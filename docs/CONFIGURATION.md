# Configuration Guide

English | [简体中文](CONFIGURATION.zh-CN.md)

This document details every section of [`configs/config.yaml`](../configs/config.yaml),
the routing model, and how sensitive-info masking works.

Configuration precedence: **environment variables (`AIGIS_*`) > command-line flags > config.yaml**.

- Default config file: `configs/config.yaml`
- Custom path: `./bin/aigis --config /path/to/config.yaml serve`

## Table of contents

- [Top-level sections](#top-level-sections)
- [Routing engine](#routing-engine)
  - [Matcher](#matcher)
  - [Upstream](#upstream)
  - [Header policy](#header-policy)
  - [Transforms](#transforms)
- [Sensitive-info masking (PII)](#sensitive-info-masking-pii)
  - [Built-in rules](#built-in-rules)
  - [Per-route rule selection & email mode](#per-route-rule-selection--email-mode)
  - [Custom rules](#custom-rules)
- [Strict egress review (force_block)](#strict-egress-review-force_block)

## Top-level sections

| Section | Key | Default | Meaning |
|---------|-----|---------|---------|
| `server` | `host` / `port` | `0.0.0.0` / `8080` | Listen address. |
| `log` | `level` | `debug` | `debug` / `info` / `warn` / `error`. |
| `log` | `file` | `./logs/aigis.log` | Log file path (also written to stdout). |
| `log` | `rotate` | `false` | Built-in lumberjack rotation. Leave off if you use system `logrotate` — never point both at the same file. |
| `log` | `max_size_mb` / `max_backups` / `max_age_days` / `compress` | `100` / `7` / `30` / `true` | Only apply when `rotate: true`. |
| `audit` | `enabled` | `true` | Append one metadata-only JSON line to `./logs/audit.jsonl` per masked request (rule type + counts + placeholders, **no plaintext**). Clean requests write nothing. |
| `limit` | `max_concurrent` | `0` | Cap on concurrent requests; excess get HTTP 429. `0` = unlimited. |
| `breaker` | `enabled` / `fail_threshold` / `cooldown_sec` | `false` / `5` / `30` | Per-route circuit breaker: after N consecutive upstream failures the route returns HTTP 503 for the cooldown, then probes half-open. |
| `cache` | `ttl_sec` / `max_entries` | `0` / `1000` | Non-streaming response cache. Identical requests within TTL return the prior response (`X-Cache: HIT`). `0` = disabled. Holds plaintext in memory — keep TTL short. |
| `security` | `custom_rules` | (none) | Global custom masking rules — see [Custom rules](#custom-rules). |
| `openai` | `api_key` / `base_url` / `model` | — | Legacy single-route fallback, used only when `engine.routes` is empty. |
| `engine` | `routes` | — | The routing table (takes precedence over `openai`). |

## Routing engine

Requests are matched against `engine.routes` **in order**; the first matching
route wins. Put the most specific routes first and a catch-all last.

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

### Matcher

`matcher` maps a request-body JSON field to a Go regexp. The common key is
`model`. An **empty matcher (`{}`) matches everything** — use it only for the
last catch-all route.

```yaml
matcher:
  model: "^gpt-.*"   # gpt-3.5-turbo, gpt-4, ...
```

### Upstream

| Field | Meaning |
|-------|---------|
| `base_url` | Upstream base URL. Supports `env:VAR` — resolved at request time from that env var (logs show the resolved URL). |
| `path` | Endpoint path (default `/chat/completions`). |
| `auth_strategy` | `bearer` (`Authorization: Bearer <token>`), `header` (custom header), `query` (`api_key` query param), or `none`. |
| `token_env` | Env var holding the token. |
| `header_name` | Header name for the `header` strategy (default `Authorization`). |

### Header policy

Controls which headers reach the upstream. Applied in order: **Allow → Set →
Remove → Auth** (auth headers always win). Useful for providers that
authenticate with a custom header instead of Bearer (e.g. Anthropic's `x-api-key`).

```yaml
header_policy:
  allow: ["anthropic-version", "content-type", "anthropic-beta"]
  set:
    "x-api-key": "env:AIGIS_ANTHROPIC_KEY"   # env:VAR resolved at request time
  remove: ["authorization"]
```

### Transforms

`transforms` is an ordered pipeline applied to the **request** body;
`response_transforms` applies to the (non-streaming) **response** body before
unmask.

| Type | Purpose |
|------|---------|
| `pii` | Mask sensitive info (tokenize → vault → unmask on the way back). |
| `pii_claude` | PII masking adapted to Anthropic `/messages` shape. |
| `template` | Reshape the body with a Go text/template (e.g. OpenAI → Dify payload). |
| `field_map` | Map fields `target: source` (e.g. `"prompt": "messages.0.content"`). |

For streaming, `stream_translate` names a translator that converts upstream SSE
back into OpenAI `chat.completion.chunk` events (e.g. `"dify"`); empty means
passthrough with cross-chunk unmask.

## Sensitive-info masking (PII)

The `pii` transform replaces each detected secret with a stable placeholder of
the form `__AIGIS_SEC_<hash>__` before the request leaves the gateway, stores
the mapping in a per-request vault, and un-masks it in the response. The same
secret maps to the same placeholder within a request.

### Built-in rules

Applied in this order (more specific patterns first):

| Rule name | Detects | Replacement label |
|-----------|---------|-------------------|
| `Private Key` | `-----BEGIN ... PRIVATE KEY-----` blocks | `[PRIVATE_KEY_REDACTED]` |
| `AWS Access Key` | `AKIA...` (20 chars) | `[AWS_AK_REDACTED]` |
| `OpenAI API Key` | `sk-` / `sk-proj-` keys | `[OPENAI_KEY_REDACTED]` |
| `GitHub Token` | `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_` tokens | `[GITHUB_TOKEN_REDACTED]` |
| `Google API Key` | `AIza...` (39 chars) | `[GOOGLE_KEY_REDACTED]` |
| `Email` | email addresses | `[EMAIL_REDACTED]` |
| `Mobile Phone` | Chinese mobile numbers (11 digits, optional `+86`) | `[PHONE_REDACTED]` |

### Per-route rule selection & email mode

The `pii` transform's `config` accepts:

- `rules` — comma-separated rule names to apply on **this** route (e.g.
  `"Email,Mobile Phone"`). Omit / empty = all rules. Names are the built-in
  names above plus any custom-rule names.
- `email` — email tokenization mode:
  - `full` (default, omitted) — tokenize the whole address (most strict).
  - `local` — tokenize only the mailbox, keep `@domain` visible to the model.
    The full address is still restored on the way back.

```yaml
transforms:
  - type: "pii"
    config:
      rules: "Email,Mobile Phone"
      email: "local"
```

### Custom rules

Add domain-specific patterns (ID cards, order numbers, ...). Each rule's
`pattern` is a Go regexp; an invalid regex **fails loud at startup**. A rule
named `"Order ID"` produces the label `[ORDER_ID_REDACTED]`.

**Global** (applies to all routes) — under `security`:

```yaml
security:
  custom_rules:
    - name: "ID Card"
      pattern: '\b\d{17}[\dXx]\b'
    - name: "Bank Card"
      pattern: '\b\d{16,19}\b'
```

**Per-route** — via the `pii` transform's `custom_rules` config key. Since a
transform's `config` values are strings, the value is a **JSON array string** of
`{name, pattern}` objects (compiled per process, cached, does not touch the
shared scanner):

```yaml
transforms:
  - type: "pii"
    config:
      custom_rules: '[{"name":"Order ID","pattern":"ORD-\\d{8}"}]'
```

## Strict egress review (force_block)

Set `force_block: true` on a route to enable a final pre-send leak check. A
streaming request (`stream: true`) is served internally via the blocking path so
the fully-masked body can be re-scanned with the **built-in** rules before
anything is sent upstream; if any built-in secret survived masking, the request
is rejected. The client still receives an SSE response (the buffered result is
re-emitted as a pseudo-stream), so it is unaware of the downgrade.

> Note: only built-in rules gate egress — route-scoped custom rules mark
> business IDs (order numbers, etc.), not leak-grade secrets, so their residue
> does not block a request.

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

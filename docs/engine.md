
问题3：3 转换引擎：我们要设计一个转换引擎，替换开发低效的 openai.go 硬编码替换，他的功能应该包括 
1) 把同一个provider的消息做PII替换或是key转换，加header 等等
2) 不同的 provider 转换，和PII替换，比如openai 转到 dify 
3) 引擎配置可以放在数据库或是直接调用

引擎配置大约如下：
  request
    from Setting: ak 
    to setting: ak base_url 
    rules: [替换手机号 替换邮箱]
    path openai dify cluade 随机 之类
  reresponse
    path /openai /dify /cluade

## rule: overwrite 补充

* 补充 (If missing, add):
       * `default` (Most common for "use this if nothing is there")
       * `fill` or fill_missing
       * `ensure` (Ensure a value exists)
       * `supplement` (Less common in code variables, more in docs)

* 替换 (If exists, replace / Force update):
       * `overwrite` (Standard for "write over regardless")
       * `override` (Common in inheritance or config layering)
       * `replace`

  Summary of logic:

   1. Overwrite / Replace: "Whether it exists or not, write the new value." (Ignoring the old value).
   2. Default / Fill: "Only write the value if the current one is empty/null."

那么一个简单的openai基本的规则如下：
overwrite api_key "Bearer "+p.apiKey
overwrite base_url 
replace message PII:phone


## Task

Task 1: Engine 配置结构 (internal/core/engine/config.go)
  - EngineConfig 包含 Routes 数组
  - Route 定义匹配器、上游、转换步骤
  - Upstream 定义 BaseURL、认证策略、Token 环境变量
  - TransformStep 定义转换类型和配置

  Task 2: Router (internal/core/engine/router.go)
  - FindRoute() 使用 gjson 解析 body，匹配正则规则
  - 返回第一个匹配的 Route

  Task 3: Universal Provider (internal/core/providers/universal.go)
  - 替代硬编码的 OpenAI Provider
  - 支持三种转换: pii, field_map, template
  - 根据 AuthStrategy 设置认证头
  - 从环境变量读取 Token

  Task 4: 配置加载
  - 更新 Viper 配置以加载 routes 结构
  - 支持复杂嵌套配置

  Task 5: 集成
  - 重构 HTTPServer 使用 Engine 查找路由
  - 动态选择 Provider

  Task 6: config.yaml 示例
  - 添加默认 OpenAI 路由 (Identity transformation)

  Task 7: 测试
  - 构建项目，验证编译通过

  ---
  请确认是否开始执行？ 如果有任何调整或疑问，请告知。

## 完成说明

● 服务正常启动，可以看到:
  - Engine 初始化成功，加载了 2 个路由
  - openai-default 路由 (匹配 ^gpt-.* 模型)
  - fallback 路由 (匹配所有)

● 实现完成

  Transformation Engine 已成功实现。以下是创建/修改的文件:

  新增文件

  | 文件                                   | 描述                                              |
  |--------------------------------------|-------------------------------------------------|
  | internal/core/engine/config.go       | Engine 配置结构定义 (Route, Upstream, TransformStep)  |
  | internal/core/engine/router.go       | Engine 核心 + Router 实现，支持 JSON path 正则匹配         |
  | internal/core/providers/universal.go | Universal Provider，支持 PII/field_map/template 转换 |

  修改文件

  | 文件                        | 变更                       |
  |---------------------------|--------------------------|
  | internal/config/config.go | 添加 LoadEngineConfig() 函数 |
  | internal/server/http.go   | 集成 Engine，使用动态路由         |
  | cmd/aigis/serve.go        | 处理 NewHTTPServer 返回的错误   |
  | configs/config.yaml       | 添加 engine.routes 配置示例    |
  | tests/integration_test.go | 适配新的 API                 |

  架构变更

  请求 → Pipeline (日志) → Engine.FindRoute() → UniversalProvider.Send()
                                                      ↓
                                            applyRequestTransforms()
                                                      ↓
                                               PII / FieldMap / Template
                                                      ↓
                                               sendToUpstream()
                                                      ↓
                                                 返回响应

  配置示例

  engine:
    routes:
      - id: "openai-default"
        matcher:
          model: "^gpt-.*"  # 正则匹配
        upstream:
          base_url: "https://api.openai.com/v1"
          path: "/chat/completions"
          auth_strategy: "bearer"
          token_env: "OPENAI_API_KEY"
        transforms:
          - type: "pii"
            config: {}

  支持的转换类型

  1. pii: PII 脱敏 (邮箱、手机号)
  2. field_map: 字段映射 (使用 gjson/sjson)
  3. template: Go text/template 转换 (用于跨 Provider 格式转换)
## gemini
这是一个非常高级且架构师级别的思考。你正在从一个“硬编码的代理”进化为一个**“通用的 API 编排网关”**。

你描述的系统非常类似企业级网关（如 Kong, Tyk）加上类似 Zapier 的转换逻辑。为了实现你想要的 **OpenAI -> Dify/Claude** 的协议转换，以及 **Dynamic Routing (动态路由)**，我们必须引入一个 **"Rule-Based Transformation Engine" (基于规则的转换引擎)**。

我们不再写 `openai.go` 或 `dify.go`，而是写一个 **`UniversalProvider`**，它根据**配置文件（或数据库配置）**来决定怎么处理数据。

---

###核心架构：AIGis 转换引擎 (Transformation Engine)我们将设计三个核心概念：

1. **Route (路由):** 决定请求由谁处理（例如：根据 Model 名称或 Path）。
2. **Upstream (上游):** 定义目标服务（BaseURL, Token 策略）。
3. **Pipeline (处理链):** 定义一系列**原子操作**（Step），如 PII 过滤、字段映射 (Map)、模板渲染 (Template)。

###1. 配置文件设计 (Schema Design)为了支持你说的 OpenAI -> Dify，最灵活的方式不是写代码，而是**配置即代码**。我们需要支持 **JSONPath (读取)** 和 **Go Template (重组)**。

假设我们要把 OpenAI 请求转发给 Dify，配置文件 `routes.yaml` 可能是这样的：

```yaml
routes:
  - id: "openai-to-dify-route"
    matcher:
      model: "dify-agent-.*" # 匹配所有以 dify-agent 开头的模型请求
    upstream:
      url: "https://api.dify.ai/v1/chat-messages"
      auth: "Bearer env:DIFY_API_KEY" # 从环境变量读 Key
    pipeline:
      request:
        - type: "pii_guard"        # 1. 先做 PII 过滤
        - type: "json_template"    # 2. 协议转换 (OpenAI -> Dify)
          template: |
            {
              "inputs": {},
              "query": "{{ .messages | last | get "content" }}", 
              "response_mode": "blocking",
              "user": "{{ .user }}"
            }
      response:
        - type: "json_map"         # 3. 响应映射 (Dify -> OpenAI)
          mapping:
            "choices.0.message.content": "answer" # 把 Dify 的 answer 字段映射回 OpenAI 的 content

```

###2. Go 结构体设计我们需要定义这些通用的结构体，而不是硬编码 OpenAI。

```go
type Route struct {
    ID        string
    Matcher   map[string]string // e.g. {"model": "gpt-4"}
    Upstream  Upstream
    Pipeline  []StepConfig
}

type StepConfig struct {
    Type   string            // "pii", "template", "map"
    Params map[string]string // 参数
}

```

###3. 实现策略* **匹配 (Matcher):** 使用 `gjson` 读取输入 JSON 的 `model` 字段，与配置进行正则匹配。
* **转换 (Transformer):**
* **Simple Map:** 使用 `sjson` 把 A 字段的值填到 B 字段。
* **Complex Template:** 使用 Go `text/template` + `Sprig` 函数库，这是最强大的，能处理 Dify 这种结构完全变了的情况。
---

```markdown
We are evolving AIGis from a hardcoded OpenAI proxy to a **Configurable Transformation Engine**.

The goal is to support dynamic routing and protocol translation (e.g., OpenAI format -> Dify format) via configuration, without changing Go code.

Please implement the **Transformation Engine** with the following specifications:

### Task 1: Define Engine Configuration (`internal/core/engine/config.go`)
Create a structured configuration to replace hardcoded logic.
```go
package engine

type EngineConfig struct {
    Routes []Route `mapstructure:"routes"`
}

type Route struct {
    ID       string            `mapstructure:"id"`
    // Matcher: Key is JSON path (e.g., "model"), Value is Regex (e.g., "^gpt-.*")
    Matcher  map[string]string `mapstructure:"matcher"` 
    Upstream Upstream          `mapstructure:"upstream"`
    // Pipeline of transformations to apply
    Transforms []TransformStep `mapstructure:"transforms"`
}

type Upstream struct {
    BaseURL string `mapstructure:"base_url"`
    // AuthStrategy: "bearer", "header", "query"
    AuthStrategy string `mapstructure:"auth_strategy"`
    // TokenEnv: The environment variable name to read the token from
    TokenEnv     string `mapstructure:"token_env"` 
}

type TransformStep struct {
    Type   string            `mapstructure:"type"` // e.g., "pii", "field_map", "template"
    Config map[string]string `mapstructure:"config"`
}

```

###Task 2: Implement the Router (`internal/core/engine/router.go`)* Implement `func (e *Engine) FindRoute(body []byte) (*Route, error)`.
* Logic:
1. Parse `body` using `sonic` or `gjson`.
2. Iterate through all configured `Routes`.
3. Check if the `Matcher` rules match the body (e.g., if body `model` matches the regex).
4. Return the first matching Route.



###Task 3: Implement the Universal Provider (`internal/core/providers/universal.go`)Replace (or augment) the `OpenAIProvider` with a `UniversalProvider`.

* **State:** It needs access to the `Route` object.
* **Send Method Logic:**
1. **Apply Request Transforms:** Iterate `Route.Transforms`.
* If Type == "pii": Call existing PII logic.
* If Type == "template": Use Go `text/template` to reconstruct the JSON body using the input data. (Crucial for OpenAI->Dify).
* If Type == "field_map": Use `gjson` to read source and `sjson` to write target.


2. **Prepare Request:**
* URL = `Route.Upstream.BaseURL`.
* Headers = Set Authorization based on `AuthStrategy` and `os.Getenv(TokenEnv)`.


3. **Send:** Execute HTTP request.
4. **Apply Response Transforms:** (Optional MVP) Allow mapping response fields back.



###Task 4: Configuration Loading (`cmd/aigis/config.go`)* Update Viper configuration to load this complex `routes` structure from `config.yaml`.
* Add a sample route in `config.yaml` that mimics standard OpenAI behavior (Identity transformation) as the default.

###Task 5: Integration* Refactor `internal/server/http.go` to use the `Engine` to find a route and execute the `UniversalProvider`.

**Action:**
This is a major architectural shift. Please implement the config structures, the router, and the basic Universal Provider logic.

```

### 此方案的优越性分析

1.  **真正的 Dify/Claude 支持：**
    Dify 的 API 需要 `inputs` 和 `query` 字段，而 OpenAI 传的是 `messages` 数组。
    通过 **Task 3** 中的 `"template"` 转换器，你可以在配置文件里写：
    `template: '{"query": "{{ .messages | last }}", "inputs": {}}'`
    这比写 Go 代码去解析数组优雅得多。

2.  **多租户/多 Key 支持：**
    `TokenEnv` 字段允许不同的 Route 使用不同的环境变量 Key（例如 `DIFY_KEY`, `OPENAI_KEY_VIP`）。

3.  **热更新潜力：**
    未来如果你把配置存到数据库（MySQL/Redis），这个引擎可以设计成每分钟重载一次配置，无需重启服务即可上线新的 Model 路由。

**你觉得这个“配置驱动引擎”的思路符合你的期望吗？** 如果符合，这个 Prompt 就可以执行了。这会把你的代码复杂度转移到配置文件上，大大提高灵活性。

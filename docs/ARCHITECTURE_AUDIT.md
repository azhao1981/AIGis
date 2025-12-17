# AIGis 架构审计报告

**审计日期**：2025-12-17
**审计目标**：识别"旧硬编码 Provider"与"新引擎转换"之间的兼容性问题
**项目状态**：混合架构（部分迁移完成）

---

## 📋 执行摘要

项目正处于从 **硬编码实现** 向 **配置驱动架构** 过渡的中间状态。已完成核心引擎的迁移，但遗留代码清理和接口职责划分尚未完成。

**架构完整性**：60%
**风险等级**：中（存在死代码，设计未充分利用）

---

## 1. 项目结构概览

```
AIGis/
├── cmd/aigis/                    # CLI 入口
│   ├── main.go
│   ├── root.go
│   └── serve.go                  # 服务启动命令
├── configs/
│   └── config.yaml              # 核心配置（引擎路由定义）
├── internal/
│   ├── config/                  # 配置加载逻辑
│   │   └── config.go
│   ├── core/                    # 核心接口与实现
│   │   ├── context.go           # 扩展上下文
│   │   ├── provider.go          # Provider 接口
│   │   ├── processor.go         # Processor 接口
│   │   ├── pipeline.go          # 处理器管道
│   │   ├── engine/              # 新引擎模块
│   │   │   ├── config.go        # 引擎配置结构
│   │   │   └── router.go        # 路由匹配逻辑
│   │   ├── processors/          # 处理器实现（仅日志）
│   │   │   ├── logger.go
│   │   │   └── pii_guard.go     # 未被使用
│   │   └── providers/           # Provider 实现
│   │       ├── openai.go        # ❌ 旧实现（已废弃）
│   │       └── universal.go     # ✅ 新实现（使用中）
│   ├── pkg/logger/              # 日志封装
│   └── server/                  # HTTP 服务
│       ├── server.go            # 基础服务
│       └── http.go              # 处理器逻辑
└── tests/
    ├── integration_test.go
    └── test_base.sh
```

---

## 2. 核心接口定义

### 2.1 Provider 接口
**文件**：`internal/core/provider.go`

```go
type Provider interface {
    ID() string
    Send(ctx context.Context, body []byte) ([]byte, error)
    Stream(ctx context.Context, body []byte) (<-chan []byte, error)
}
```

**职责**：统一 LLM 适配器接口，处理原始字节请求/响应

---

### 2.2 Processor 接口
**文件**：`internal/core/processor.go`

```go
type Processor interface {
    Name() string
    Priority() int
    OnRequest(ctx *AIGisContext, body []byte) ([]byte, error)
    OnResponse(ctx *AIGisContext, body []byte) ([]byte, error)
}
```

**职责**：中间件接口，用于请求/响应拦截和转换

**当前实际用途**：
- ✅ 仅用于 `RequestLogger`（日志记录）
- ❌ 未用于 PII 处理、字段映射等转换

---

### 2.3 AIGisContext
**文件**：`internal/core/context.go`

```go
type AIGisContext struct {
    context.Context
    RequestID string
    UserID    string
    TraceID   string
    StartTime time.Time
    Log       *zap.Logger

    mu       sync.RWMutex
    metadata map[string]interface{}  // 线程安全元数据
}
```

**职责**：贯穿请求处理全生命周期的扩展上下文

---

## 3. 新架构：引擎与转换系统

### 3.1 引擎配置结构
**文件**：`internal/core/engine/config.go`

```go
type EngineConfig struct {
    Routes []Route
}

type Route struct {
    ID         string            // 路由唯一标识
    Matcher    map[string]string // JSON Path → 正则表达式
    Upstream   Upstream          // 上游服务配置
    Transforms []TransformStep   // 转换管道
}

type Upstream struct {
    BaseURL      string // 基础 URL
    Path         string // 端点路径
    AuthStrategy string // bearer/header/query
    TokenEnv     string // Token 环境变量名
    HeaderName   string // 自定义 Header 名称
}

type TransformStep struct {
    Type   string            // pii/field_map/template
    Config map[string]string // 类型特定配置
}
```

**支持的转换类型**：
- `pii`：PII 脱敏（邮箱、手机号）
- `field_map`：字段映射（gjson/sjson）
- `template`：Go text/template 变换

---

### 3.2 路由匹配引擎
**文件**：`internal/core/engine/router.go`

```go
type Engine struct {
    config   *EngineConfig
    matchers map[string]map[string]*regexp.Regexp
}

// 匹配逻辑：
// 1. 解析请求 JSON
// 2. 遍历所有路由
// 3. 检查所有 Matcher 是否匹配
// 4. 返回第一个匹配的路由（或 nil）
func (e *Engine) FindRoute(body []byte) (*Route, error)
```

**性能优化**：所有正则在引擎启动时预编译并缓存

---

### 3.3 通用 Provider
**文件**：`internal/core/providers/universal.go`

```go
type UniversalProvider struct {
    route  *engine.Route
    client *http.Client
}

func (p *UniversalProvider) Send(ctx context.Context, body []byte) ([]byte, error) {
    // 1. 应用请求转换
    transformedBody := p.applyRequestTransforms(body)

    // 2. 发送到上游
    respBody := p.sendToUpstream(ctx, transformedBody)

    // 3. 应用响应转换（暂未实现）
    return respBody, nil
}

// 内部转换实现：
// - applyPIITransform
// - applyFieldMapTransform
// - applyTemplateTransform
```

---

## 4. 旧架构：硬编码 Provider（已废弃）

### 4.1 OpenAIProvider
**文件**：`internal/core/providers/openai.go`

```go
type OpenAIProvider struct {
    apiKey  string
    baseURL string
    client  *http.Client
}

func (p *OpenAIProvider) Send(ctx context.Context, body []byte) ([]byte, error) {
    // 硬编码：固定 URL = baseURL + "/chat/completions"
    // 硬编码：固定 Header = "Bearer " + apiKey
    // 无转换能力
}
```

**状态**：❌ **死代码**（项目中从未被使用）

---

## 5. 服务集成：关键连线逻辑

### 5.1 HTTP 处理器流程
**文件**：`internal/server/http.go:141-224`

```go
func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    // 步骤 1: 读取原始请求体
    body, _ := io.ReadAll(r.Body)

    // 步骤 2: 执行 Pipeline（仅日志）
    // 注意：此处不进行任何转换
    processedBody, _ := s.pipeline.ExecuteRequest(ctx, body)

    // 步骤 3: Engine 匹配路由
    route, _ := s.engine.FindRoute(processedBody)
    if route == nil {
        http.Error(w, "No matching route", http.StatusNotFound)
        return
    }

    // 步骤 4: 创建 UniversalProvider
    provider := providers.NewUniversalProvider(route)

    // 步骤 5: Provider 处理（转换 + 发送）
    // 这里才是真正的 PII/字段映射/模板转换发生的地方
    resp, _ := provider.Send(r.Context(), processedBody)

    // 步骤 6: Pipeline 处理响应（仅日志）
    finalResp, _ := s.pipeline.ExecuteResponse(ctx, resp)

    w.WriteHeader(http.StatusOK)
    w.Write(finalResp)
}
```

**执行顺序可视化**：
```
HTTP Request
    ↓
[1. Pipeline 请求阶段] → 仅日志记录
    ↓
[2. Engine 路由匹配] → 查找 Route 配置
    ↓
[3. Provider.Send] → 转换管道 + 上游通信
    │   ├─ applyPIITransform
    │   ├─ applyFieldMapTransform
    │   └─ applyTemplateTransform
    ↓
[4. Pipeline 响应阶段] → 仅日志记录
    ↓
HTTP Response
```

---

### 5.2 服务初始化流程
**文件**：`internal/server/http.go:34-82`

```go
func NewHTTPServer(addr string, zapLogger *zap.Logger) (*HTTPServer, error) {
    // 1. 创建基础服务器
    baseServer := New(addr)

    // 2. 创建 Pipeline（仅包含 RequestLogger）
    pipeline := core.NewPipeline()
    pipeline.AddProcessor(processors.NewRequestLogger())

    // 3. 加载引擎配置（关键步骤）
    engineConfig, _ := config.LoadEngineConfig()

    // 4. 创建引擎（预编译所有正则）
    eng, _ := engine.NewEngine(engineConfig)

    // 5. 记录配置
    for _, route := range engineConfig.Routes {
        logger.Info("Route configured", ...)
    }

    return &HTTPServer{
        Server:   baseServer,
        pipeline: pipeline,
        engine:   eng,
        logger:   extLogger,
    }, nil
}
```

---

### 5.3 配置加载逻辑
**文件**：`internal/config/config.go:77-114`

```go
func LoadEngineConfig() (*engine.EngineConfig, error) {
    var config engine.EngineConfig

    // 从 config.yaml 读取 engine.routes
    if err := viper.UnmarshalKey("engine", &config); err != nil {
        return nil, err
    }

    // 向后兼容：如果没有 routes，创建默认 OpenAI 路由
    if len(config.Routes) == 0 {
        config.Routes = []engine.Route{{
            ID:      "default-openai",
            Matcher: map[string]string{}, // 匹配所有
            Upstream: engine.Upstream{
                BaseURL:      viper.GetString("openai.base_url"),
                Path:         "/chat/completions",
                AuthStrategy: engine.AuthStrategyBearer,
                TokenEnv:     "OPENAI_API_KEY",
            },
            Transforms: []engine.TransformStep{{
                Type:   engine.TransformTypePII,
                Config: map[string]string{},
            }},
        }}

        // 使用旧配置中的 API Key
        if apiKey := viper.GetString("openai.api_key"); apiKey != "" {
            os.Setenv("OPENAI_API_KEY", apiKey)
        }
    }

    return &config, nil
}
```

---

## 6. 配置文件详解

**文件**：`configs/config.yaml`

```yaml
server:
  host: "0.0.0.0"
  port: 8080

log:
  level: "info"

# 旧配置（仅后备）
openai:
  api_key: ""          # 支持遗留方式
  base_url: "https://api.openai.com/v1"
  model: "gpt-3.5-turbo"

# 新配置（优先级更高）
engine:
  routes:
    # 路由 1: OpenAI 兼容服务（带 PII 脱敏）
    - id: "openai-default"
      matcher:
        model: "^gpt-.*"    # 正则匹配 gpt-3.5, gpt-4 等
      upstream:
        base_url: "https://aihubmix.com/v1"
        path: "/chat/completions"
        auth_strategy: "bearer"
        token_env: "AIGIS_OPENAI_API_KEY"
      transforms:
        - type: "pii"
          config: {}   # 使用默认模式

    # 路由 2: Dify 工作流（注释示例）
    # - id: "dify-workflow"
    #   matcher:
    #     model: "^dify-.*"
    #   upstream:
    #     base_url: "https://api.dify.ai/v1"
    #     path: "/workflows/run"
    #     auth_strategy: "bearer"
    #     token_env: "DIFY_API_KEY"
    #   transforms:
    #     - type: "pii"
    #       config: {}
    #     - type: "template"
    #       config:
    #         template: |
    #           {
    #             "inputs": {
    #               "query": "{{index .messages 0 \"content\"}}"
    #             },
    #             "response_mode": "blocking",
    #             "user": "{{.user}}"
    #           }

    # 路由 3: 字段映射示例
    # - id: "custom-api"
    #   matcher:
    #     model: "^custom-.*"
    #   upstream:
    #     base_url: "https://custom-api.example.com"
    #     path: "/generate"
    #     auth_strategy: "header"
    #     header_name: "X-API-Key"
    #     token_env: "CUSTOM_API_KEY"
    #   transforms:
    #     - type: "field_map"
    #       config:
    #         "prompt": "messages.0.content"  # 目标: 源
    #         "max_tokens": "max_tokens"

    # 路由 4: 兜底路由（必须在最后）
    - id: "fallback"
      matcher: {}      # 空匹配器 = 匹配所有
      upstream:
        base_url: "https://api.openai.com/v1"
        path: "/chat/completions"
        auth_strategy: "bearer"
        token_env: "OPENAI_API_KEY"
      transforms:
        - type: "pii"
          config: {}
```

**配置优先级**：
1. `engine.routes`（最高优先级）
2. `openai.*`（仅在 `engine.routes` 为空时使用）

---

## 7. 关键兼容性问题

### 7.1 Pipeline 职责冲突 ⚠️

**问题描述**：
- `Processor` 接口设计为通用中间件
- 但 `Pipeline` 只用于日志记录
- 真正的转换逻辑下沉到 `UniversalProvider` 内部

```go
// 设计意图：Pipeline 可扩展
pipeline.AddProcessor(NewPIIProcessor())  // ❌ 未实现

// 实际实现：Provider 硬编码
// universal.go:67-89
func (p *UniversalProvider) applyRequestTransforms(body []byte) ([]byte, error) {
    for _, step := range p.route.Transforms {
        switch step.Type {
        case engine.TransformTypePII:
            result, err = p.applyPIITransform(result, step.Config)
        // ...
        }
    }
}
```

**影响**：
- ❌ 无法通过 Pipeline 扩展新转换类型
- ❌ 转换逻辑与 Provider 强耦合
- ❌ Processor 接口未被充分利用

**建议**：架构需要明确决策，要么：
- **选项 A**：移除 Processor 接口，专注 Provider 内部实现
- **选项 B**：将转换逻辑迁移到 Pipeline，Provider 只负责通信

---

### 7.2 死代码（OpenAIProvider）❌

**文件**：`internal/core/providers/openai.go`

**问题**：
- 整个文件未被任何代码引用
- 保留了旧架构的实现
- 造成维护负担

**验证**：
```bash
$ grep -r "OpenAIProvider" --include="*.go" .
# 仅在 openai.go 自身找到定义
```

**建议**：删除此文件及相关引用

---

### 7.3 接口设计未充分利用 ⚠️

**Processor 接口**：
```go
type Processor interface {
    Name() string
    Priority() int
    OnRequest(ctx *AIGisContext, body []byte) ([]byte, error)
    OnResponse(ctx *AIGisContext, body []byte) ([]byte, error)
}
```

**实际实现**：
```go
// internal/core/processors/logger.go
type RequestLogger struct{}  // 仅日志

// internal/core/processors/pii_guard.go
type PIIGuard struct{}      // ❌ 有实现但未被使用
```

**现状统计**：
- ✅ 接口定义良好
- ✅ 可扩展设计
- ❌ 只有 1 个实际实现（日志）
- ❌ `PIIGuard` 文件存在但**完全未被调用**

**影响**：
- 潜在的架构价值未被利用
- 转换逻辑位置混乱

---

### 7.4 配置加载路径复杂 ⚠️

**当前流程**：
```
configs/config.yaml
    ↓
viper（在 cmd/aigis/serve.go 初始化）
    ↓
config.LoadEngineConfig()  // internal/config/config.go
    ↓
engine.NewEngine()         // internal/core/engine/
```

**问题点**：
- 配置加载分散在多层
- `internal/config/config.go` 依赖 `internal/core/engine`（循环依赖风险）
- 缺少配置验证层

**建议**：统一配置解析位置

---

## 8. 新旧架构对比

| 特性 | 旧架构 (OpenAIProvider) | 新架构 (UniversalProvider) |
|------|------------------------|---------------------------|
| **核心实现** | `openai.go` (硬编码) | `universal.go` (配置驱动) |
| **路由能力** | ❌ 无（固定目标） | ✅ Engine + 正则匹配 |
| **PII 脱敏** | ❌ 无 | ✅ `type: "pii"` |
| **字段映射** | ❌ 无 | ✅ `type: "field_map"` |
| **模板转换** | ❌ 无 | ✅ `type: "template"` |
| **扩展性** | 需改代码 | 配置即服务 |
| **向后兼容** | - | 通过默认路由实现 |
| **配置优先级** | 环境变量/Config | Engine.routes > openai.* |
| **Pipeline 利用** | 可能用于变换 | 仅日志 |

---

## 9. 架构决策建议

### 9.1 立即清理（建议优先级：高）

**移除死代码**：
```bash
rm internal/core/providers/openai.go
```

**更新文档**：
- 移除 `openai.go` 的所有引用
- 更新 README 和 CLAUDE.md

---

### 9.2 架构选择（必须二选一）

#### **选项 A：保留 Pipeline 架构**（推荐，符合开闭原则）

**架构图**：
```
HTTP Request
    ↓
Pipeline (可扩展中间件)
    ├─ RequestLogger
    ├─ PIIGuard         ← 新增
    ├─ FieldMapper      ← 新增
    └─ TemplateEngine   ← 新增
    ↓
Engine (仅路由匹配)
    ↓
Provider (仅通信，无转换)
    ↓
Pipeline (响应)
    ↓
HTTP Response
```

**优势**：
- ✅ 转换逻辑解耦
- ✅ 符合单一职责原则
- ✅ 易于扩展新转换器

**工作量**：
- 将 `universal.go` 的转换逻辑提取为独立 Processor
- 修改 `Processor` 接口（可能需要）

---

#### **选项 B：简化架构**（推荐，短期快速）

**架构图**：
```
HTTP Request
    ↓
Logger (简单日志)
    ↓
Engine (路由 + 匹配)
    ↓
Provider (转换 + 通信)
    ↓
Logger
    ↓
HTTP Response
```

**移除**：
- `internal/core/processor.go`
- `internal/core/pipeline.go`
- `internal/core/processors/pii_guard.go`

**优势**：
- ✅ 代码更简洁
- ✅ 减少间接层
- ✅ 适合当前阶段

---

### 9.3 配置验证层

**缺失功能**：
```go
// 建议添加
type ConfigValidator interface {
    Validate(config EngineConfig) error
}

// 验证规则示例：
// 1. Route ID 必须唯一
// 2. Matcher 正则必须合法
// 3. Transform 类型必须已知
// 4. 兜底路由必须存在
// 5. AuthStrategy 必须有效
```

---

## 10. 测试覆盖建议

### 10.1 必须测试的场景

```go
// 1. 路由匹配测试
- Model 匹配：gpt-4, gpt-3.5-turbo, claude-3
- 无匹配时返回 nil
- 兜底路由行为

// 2. 转换测试
- PII：邮箱脱敏、手机号脱敏
- FieldMap：嵌套路径、类型转换
- Template：Go template 语法、JSON 输出验证

// 3. 错误处理
- 无效正则
- 上游 4xx/5xx
- 配置缺失

// 4. 集成测试
- 端到端请求流程
- 多路由共存
- 性能（预编译正则）
```

---

## 11. 文档缺失清单

- ❌ `docs/engine-claude.md` → 需要更新为架构日志
- ❌ `README.md` → 需要添加配置示例和架构说明
- ❌ `tests/` → 需要集成测试脚本更新

---

## 12. 总结与行动计划

### 当前状态：🔴 临界迁移期

| 维度 | 状态 | 说明 |
|------|------|------|
| 核心功能 | ✅ 可用 | UniversalProvider + Engine 已就绪 |
| 配置驱动 | ✅ 完成 | 通过 config.yaml 可扩展 |
| 代码质量 | ⚠️ 混合 | 存在死代码，接口未充分利用 |
| 执行效率 | ✅ 良好 | 正则预编译，无重复解析 |
| 可扩展性 | ⚠️ 有阻碍 | Pipeline 设计未被利用 |
| 文档完整性 | ⚠️ 低 | 缺少配置示例和架构说明 |

---

### 建议行动按优先级：

#### **立即执行（1小时内）**
1. ✅ **删除 `internal/core/providers/openai.go`**
2. ✅ **更新本文档至 `docs/ARCHITECTURE_AUDIT.md`**

#### **短期（1天内）**
3. 修复 `PIIGuard` 未被使用的 bug
4. 完善测试覆盖（至少 80%）
5. 更新 README 添加配置示例

#### **中期（3天内）**
6. 架构决策：Pipeline 还是简化（见 9.2）
7. 添加配置验证层
8. 完善文档（架构图、设计决策）

#### **长期**
9. 考虑流式支持（当前返回 `fmt.Errorf("streaming not implemented")`）
10. 性能优化（内存池、并发控制）

---

## 附录：配置示例

### A. 多提供商路由配置

```yaml
engine:
  routes:
    # 1. OpenAI/兼容服务
    - id: "openai-compatible"
      matcher: { model: "^gpt-.*" }
      upstream:
        base_url: "https://aihubmix.com/v1"
        path: "/chat/completions"
        auth_strategy: "bearer"
        token_env: "OPENAI_API_KEY"
      transforms:
        - type: "pii"
          config: {}

    # 2. Azure OpenAI
    - id: "azure-openai"
      matcher: { model: "^azure-.*" }
      upstream:
        base_url: "https://my-resource.openai.azure.com/openai/deployments/gpt-4"
        path: "/chat/completions?api-version=2024-02-15-preview"
        auth_strategy: "header"
        header_name: "api-key"
        token_env: "AZURE_API_KEY"
      transforms:
        - type: "field_map"
          config:
            "messages": "messages"  # 原样传递

    # 3. Dify 工作流
    - id: "dify-workflow"
      matcher: { model: "^dify-.*" }
      upstream:
        base_url: "https://api.dify.ai/v1"
        path: "/workflows/run"
        auth_strategy: "bearer"
        token_env: "DIFY_API_KEY"
      transforms:
        - type: "pii"
          config: {}
        - type: "template"
          config:
            template: |
              {
                "inputs": {
                  "query": "{{index .messages 0 \"content\"}}"
                },
                "response_mode": "blocking",
                "user": "{{.user}}"
              }

    # 4. 本地自定义 API
    - id: "local-custom"
      matcher: { model: "^custom.*" }
      upstream:
        base_url: "http://localhost:8000"
        path: "/generate"
        auth_strategy: "query"
        token_env: "CUSTOM_API_KEY"
      transforms:
        - type: "field_map"
          config:
            "prompt": "messages.0.content"
            "max_tokens": "max_tokens"
            "temperature": "temperature"

    # 5. 兜底路由（必须最后）
    - id: "fallback"
      matcher: {}  # 匹配所有
      upstream:
        base_url: "https://api.openai.com/v1"
        path: "/chat/completions"
        auth_strategy: "bearer"
        token_env: "OPENAI_API_KEY"
      transforms:
        - type: "pii"
          config: {}
```

---

### B. 环境变量配置

```bash
# 服务配置
AIGIS_SERVER_HOST=0.0.0.0
AIGIS_SERVER_PORT=8080
AIGIS_LOG_LEVEL=info

# API Keys
OPENAI_API_KEY=sk-...
AIGIS_OPENAI_API_KEY=sk-...  # 路由专用
AZURE_API_KEY=azure-...
DIFY_API_KEY=dify-...
CUSTOM_API_KEY=custom-...
```

---

### C. 测试命令

```bash
# 1. PII 脱敏测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {
        "role": "user",
        "content": "My email is dangerous@coder.com and my phone is 13800138000."
      }
    ]
  }'

# 预期响应：内容中的 PII 被脱敏

# 2. 路由匹配测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{"model": "gpt-4", "messages": [...]}'
# → 路由: openai-default → aihubmix.com

curl -X POST http://localhost:8080/v1/chat/completions \
  -d '{"model": "dify-xxx", "messages": [...]}'
# → 路由: dify-workflow → dify.ai

# 3. 健康检查
curl http://localhost:8080/health
# → {"status":"ok"}
```

---

**文档版本**：v1.0
**最后更新**：2025-12-17
**审计人**：Claude Code (mimo-v2-flash)

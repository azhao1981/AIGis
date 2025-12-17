# AIGis 安全文档

> **版本**: 1.0
> **最后更新**: 2025-12-17
> **状态**: ⚠️ 生产环境部署前必须完成安全加固

---

## 📋 目录

- [安全声明](#安全声明)
- [严重等级定义](#严重等级定义)
- [已知安全风险](#已知安全风险)
  - [高危风险](#高危风险)
  - [中危风险](#中危风险)
  - [低危风险](#低危风险)
- [安全加固指南](#安全加固指南)
- [安全配置最佳实践](#安全配置最佳实践)
- [部署检查清单](#部署检查清单)
- [应急响应](#应急响应)
- [漏洞报告](#漏洞报告)

---

## 🔒 安全声明

**⚠️ 重要提示**: AIGis 目前处于 Beta 阶段，存在已知的安全问题。

**请在完成本文档中的所有 "P0" 和 "P1" 安全加固前，不要在生产环境部署。**

---

## 🎯 严重等级定义

| 等级 | 描述 | 影响 |
|------|------|------|
| 🔴 **P0 - 关键** | 立即修复，不能上线 | 数据泄露、服务完全瘫痪、法律风险 |
| 🟠 **P1 - 高危** | 必须修复才能上线 | 未授权访问、费用损失、DDoS |
| 🟡 **P2 - 中危** | 建议修复 | 信息泄露、可用性影响 |
| 🟢 **P3 - 低危** | 优化建议 | 最佳实践、文档完善 |

---

## ⚠️ 已知安全风险

### 🔴 高危风险

#### 1. 缺少认证和授权机制
**风险等级**: P0
**位置**: `internal/server/http.go:141-224`

**问题描述**:
- `/v1/chat/completions` 端点没有任何身份验证
- 任何人都可以访问 AI 网关并消耗上游 API 配额
- 无法追踪真实用户，无访问控制

**攻击场景**:
```bash
# 任何人都可以发送请求
curl -X POST http://your-server:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 费用攻击：攻击者可以发送大量请求消耗你的 API 费用
# 资源占用攻击：占用你的 LLM 并发配额
```

**修复方案**:
```go
// 方案1: API Key 认证
func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    apiKey := r.Header.Get("X-API-Key")
    if apiKey == "" {
        apiKey = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
    }

    if !validateAPIKey(apiKey) {
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }
    // ... 继续处理
}

// 方案2: JWT Token 认证 (推荐用于多用户场景)
// 方案3: IP 白名单 (适合内部服务调用)
```

**优先级**: 🔴 必须在生产部署前完成

---

#### 2. 服务端请求伪造 (SSRF)
**风险等级**: P0
**位置**: `internal/core/providers/universal.go:232-292`

**问题描述**:
- `upstream.base_url` 完全由 config.yaml 配置
- 攻击者通过修改配置可以访问内部网络资源

**攻击场景**:
```yaml
# 恶意配置示例

# 访问云元数据服务
engine:
  routes:
    - id: "ssrf-aws"
      upstream:
        base_url: "http://169.254.169.254"  # AWS 元数据
        path: "/latest/meta-data/iam/security-credentials"

# 访问内网服务
    - id: "ssrf-internal"
      upstream:
        base_url: "http://10.0.0.1:8080"  # 内网 API
        path: "/admin"

# 访问本地文件 (如果存在文件处理逻辑)
    - id: "ssrf-file"
      upstream:
        base_url: "file:///"
        path: "/etc/passwd"
```

**修复方案**:
```go
var blockedIPRanges = []*net.IPNet{
    mustParseCIDR("127.0.0.0/8"),      // Loopback
    mustParseCIDR("10.0.0.0/8"),       // Private
    mustParseCIDR("172.16.0.0/12"),    // Private
    mustParseCIDR("192.168.0.0/16"),   // Private
    mustParseCIDR("169.254.0.0/16"),   // Link-local
    mustParseCIDR("0.0.0.0/8"),        // Invalid
}

func validateUpstreamURL(rawURL string) error {
    u, err := url.Parse(rawURL)
    if err != nil {
        return fmt.Errorf("invalid URL: %w", err)
    }

    // 只允许 HTTPS
    if u.Scheme != "https" {
        return fmt.Errorf("only HTTPS allowed, got: %s", u.Scheme)
    }

    // IP 白名单检查
    ips, err := net.LookupIP(u.Hostname())
    if err != nil {
        return fmt.Errorf("DNS lookup failed: %w", err)
    }

    for _, ip := range ips {
        for _, blocked := range blockedIPRanges {
            if blocked.Contains(ip) {
                return fmt.Errorf("blocked IP: %s", ip)
            }
        }
    }

    return nil
}
```

**优先级**: 🔴 必须在生产部署前完成

---

#### 3. 未限制请求体大小
**风险等级**: P0
**位置**: `internal/server/http.go:153`

**问题描述**:
- `io.ReadAll(r.Body)` 无大小限制
- 可导致 OOM (内存耗尽) DoS 攻击

**攻击场景**:
```bash
# 发送超大请求体
curl -X POST http://your-server:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "'$(python -c 'print("A"*1024*1024*100)')'"}]}'
# 100MB 数据将完全加载到内存
```

**修复方案**:
```go
func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    // 限制请求体大小 (例如: 10MB)
    maxBytes := int64(10 * 1024 * 1024)
    r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
        return
    }
    // ... 继续处理
}
```

**优先级**: 🔴 必须在生产部署前完成

---

#### 4. 模板注入漏洞
**风险等级**: P0/P1
**位置**: `internal/core/providers/universal.go:199-229`

**问题描述**:
- Go `text/template` 可访问环境变量和危险函数
- 模板配置可由用户控制 (config.yaml)

**风险函数**:
- `.Env` - 读取环境变量
- `printf` - 格式化输出
- `html` - HTML 转义
- `js` - JS 转义

**攻击场景**:
```yaml
# config.yaml
transforms:
  - type: "template"
    config:
      template: |
        {
          "api_key": "{{.Env.OPENAI_API_KEY}}",  # 泄露环境变量
          "secret": "{{printf "%q" .Env}}"       # 打印所有环境变量
        }
```

**修复方案**:
```go
// 方案1: 限制模板可用的函数
func applyTemplateTransform(body []byte, config map[string]string) ([]byte, error) {
    tmplStr := config["template"]

    // 只允许安全的模板函数
    funcMap := template.FuncMap{
        "index": func(m map[string]interface{}, key string) interface{} {
            return m[key]
        },
        // 不要添加 Env, printf 等危险函数
    }

    tmpl, err := template.New("transform").Funcs(funcMap).Parse(tmplStr)
    // ...
}

// 方案2: 完全避免使用 text/template，改用简单的字段映射
// 方案3: 沙箱执行 (复杂)
```

**优先级**: 🟠 如果使用了模板变换则必须修复

---

### 🟠 高危风险

#### 5. 敏感信息可能泄露到日志
**风险等级**: P1
**位置**:
- `internal/pkg/logger/logger.go:93-104`
- `internal/core/processors/logger.go`

**问题描述**:
- 日志可能包含请求/响应内容
- 生产环境日志文件可能被未授权访问

**修复方案**:
```go
// 1. 生产环境禁用 debug 日志
// 2. 对日志字段进行脱敏
func sanitizeLogFields(fields ...zap.Field) []zap.Field {
    sanitized := make([]zap.Field, 0, len(fields))
    for _, f := range fields {
        if f.Key == "content" || f.Key == "message" {
            f.String = "[REDACTED]"
        }
        sanitized = append(sanitized, f)
    }
    return sanitized
}

// 3. 确保日志文件权限正确
// 4. 日志分割和保留策略
```

**优先级**: 🟠 生产环境必须配置

---

#### 6. 环境变量路径遍历
**风险等级**: P1
**位置**: `internal/config/config.go:16-36`

**问题描述**:
- 向上递归查找 `.env` 可能加载项目外的敏感配置
- 在共享环境或容器中可能读取到不期望的 `.env`

**修复方案**:
```go
func findEnvFile() string {
    dir, err := os.Getwd()
    if err != nil {
        return ""
    }

    limit := 5 // 限制向上查找深度
    for i := 0; i < limit; i++ {
        envFile := filepath.Join(dir, ".env")
        if _, err := os.Stat(envFile); err == nil {
            // 检查是否在项目根目录内
            if isInsideProjectRoot(dir) {
                return envFile
            }
        }

        parent := filepath.Dir(dir)
        if parent == dir {
            break
        }
        dir = parent
    }
    return ""
}
```

**优先级**: 🟠 建议在生产部署前修复

---

### 🟡 中危风险

#### 7. 正则表达式 DoS
**风险等级**: P2
**位置**:
- `internal/core/processors/pii_guard.go:22-25`
- `internal/core/providers/universal.go:94-101`

**问题描述**:
- 默认的正则表达式在极端情况下可能性能很差
- 攻击者可构造特殊输入导致 CPU 100%

**修复方案**:
```go
// 1. 限制输入长度
const maxContentLength = 10000

func (p *PIIGuard) OnRequest(ctx *core.AIGisContext, body []byte) ([]byte, error) {
    if len(body) > maxContentLength {
        return body, nil // 跳过过长的内容
    }
    // ...
}

// 2. 使用更简单的正则
emailPattern := `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`
// 避免嵌套量词和回溯
```

**优先级**: 🟡 正常使用风险较低

---

#### 8. 缺少 CORS 控制
**风险等级**: P2
**位置**: `internal/server/http.go:89-104`

**问题描述**:
- 未设置 CORS 头，浏览器可跨域访问
- 可能被恶意网站利用

**修复方案**:
```go
// 只允许特定域名
func allowCORS(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        origin := r.Header.Get("Origin")
        allowedOrigins := []string{"https://your-app.com", "https://admin.your-app.com"}

        if contains(allowedOrigins, origin) {
            w.Header().Set("Access-Control-Allow-Origin", origin)
            w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
            w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
        }

        if r.Method == "OPTIONS" {
            w.WriteHeader(http.StatusOK)
            return
        }

        next.ServeHTTP(w, r)
    })
}
```

**优先级**: 🟡 如需浏览器访问则修复

---

### 🟢 低危风险

#### 9. 配置文件中的 API Key 示例
**风险等级**: P3
**位置**: `configs/config.yaml:10`

**问题**: 配置文件包含 `api_key: ""` 提示

**建议**:
- 配置文件中不要提及 API Key
- README 中明确说明使用环境变量
- `.env.example` 文件作为模板

---

#### 10. 错误信息暴露内部细节
**风险等级**: P3
**位置**: `internal/server/http.go:186-189`

**问题**: 路由匹配错误等内部信息返回给客户端

**修复**:
```go
// 生产环境返回通用错误
if err != nil {
    reqLogger.Error("Route matching error", zap.Error(err))
    http.Error(w, "Bad Request", http.StatusBadRequest)  // 不返回具体错误
    return
}
```

---

#### 11. 缺少速率限制
**风险等级**: P3
**位置**: 全端点

**建议方案**:
```go
// 使用第三方库或实现简单漏桶
type RateLimiter struct {
    visits map[string]*time.Time
    mu     sync.RWMutex
}

func (rl *RateLimiter) Allow(ip string) bool {
    // 简单实现: 每分钟 60 次请求
    // 建议使用成熟的库如 golang.org/x/time/rate
}
```

---

#### 12. .gitignore 问题
**风险等级**: P3
**位置**: `.gitignore:8`

**问题**: `CLAUDE.md` 被忽略，但它是项目文档

**修复**: 从 `.gitignore` 移除 `CLAUDE.md`

---

## 🛡️ 安全加固指南

### 部署前必须完成

#### 1. 实现认证机制

**选项 A: 简单 API Key (单用户/内部使用)**
```go
// 配置多个合法的 API Key
var validAPIKeys = map[string]bool{
    os.Getenv("AIGIS_API_KEY"): true,
}

func validateAPIKey(key string) bool {
    return validAPIKeys[key]
}
```

**选项 B: JWT (多用户/外部访问)**
```go
// 使用 github.com/golang-jwt/jwt/v5
func validateJWT(tokenString string) (*jwt.Token, error) {
    return jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
        if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, fmt.Errorf("unexpected signing method")
        }
        return []byte(os.Getenv("JWT_SECRET")), nil
    })
}
```

#### 2. 验证 Upstream URL

```go
func validateRouteConfig(route engine.Route) error {
    // 1. 检查 URL 格式
    u, err := url.Parse(route.Upstream.BaseURL)
    if err != nil {
        return fmt.Errorf("invalid base_url: %w", err)
    }

    // 2. 强制 HTTPS
    if u.Scheme != "https" {
        return fmt.Errorf("only HTTPS allowed")
    }

    // 3. 验证 DNS 解析
    ips, err := net.LookupIP(u.Hostname())
    if err != nil {
        return fmt.Errorf("DNS lookup failed: %w", err)
    }

    // 4. 检查私有 IP
    blocked := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16"}
    for _, ip := range ips {
        for _, cidr := range blocked {
            _, ipnet, _ := net.ParseCIDR(cidr)
            if ipnet.Contains(ip) {
                return fmt.Errorf("blocked private IP: %s", ip)
            }
        }
    }

    return nil
}
```

#### 3. 限制请求大小

```go
const MaxRequestBodySize = 10 * 1024 * 1024 // 10MB

func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)
    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
        return
    }
    // ...
}
```

#### 4. 安全日志配置

```yaml
# configs/config.yaml
log:
  level: "info"  # 生产环境不要用 "debug"

# 确保 .env 文件
# AIGIS_LOG_LEVEL=info
```

```go
// logger.go 中确保生产环境不记录敏感数据
```

---

### 生产环境推荐配置

```yaml
# configs/production.yaml

server:
  host: "127.0.0.1"  # 不对外暴露，通过 Nginx 代理
  port: 8080

log:
  level: "info"
  # 建议输出到文件并设置权限
  # output: "/var/log/aigis/app.log"

security:
  # API Key 配置 (在生产环境使用环境变量)
  api_keys:
    - "${AIGIS_API_KEY}"

  # 禁用模板变换 (如不需要)
  disable_transforms: ["template"]

  # 请求大小限制
  max_request_size: "10MB"

  # 速率限制
  rate_limit:
    requests_per_minute: 60

engine:
  routes:
    - id: "openai-production"
      matcher:
        model: "^gpt-.*"
      upstream:
        base_url: "https://api.openai.com/v1"
        path: "/chat/completions"
        auth_strategy: "bearer"
        token_env: "OPENAI_API_KEY"
      transforms:
        - type: "pii"
          config: {}
      # 没有 template 变换更安全
```

---

## 📋 部署检查清单

### P0 - 必须完成 (阻止上线)
- [ ] 实现 API Key 或 JWT 认证
- [ ] 验证并限制 upstream URL
- [ ] 设置请求体大小限制
- [ ] 禁用 debug 日志级别

### P1 - 强烈建议
- [ ] 配置生产环境变量
- [ ] 设置正确的文件权限
- [ ] 配置日志文件权限 (如果写入文件)
- [ ] 修复 .gitignore
- [ ] 设置错误信息为通用消息

### P2 - 推荐优化
- [ ] 添加速率限制
- [ ] 配置 CORS 策略
- [ ] 实现健康检查认证
- [ ] 设置监控和告警

### P3 - 长期改进
- [ ] 定期安全审计
- [ ] 漏洞扫描
- [ ] 安全测试用例
- [ ] 文档完善

---

## 🔍 安全测试建议

```bash
# 1. 认证测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'
# 预期: 401 Unauthorized

# 2. 请求大小测试
python -c 'print("{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\""+"A"*11000000+"\"}]}")' \
  | curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d @-
# 预期: 413 Request Entity Too Large

# 3. SSRF 测试 (临时修改config)
# 测试访问 127.0.0.1 或内网 IP
# 预期: 连接被拒绝

# 4. 模板注入测试
# 配置模板: {{.Env.OPENAI_API_KEY}}
# 预期: 无法读取环境变量或模板执行失败
```

---

## 🚨 漏洞报告

如果发现安全漏洞，请通过以下方式报告:

**邮箱**: [security@aigis.example.com](mailto:security@aigis.example.com)
**响应时间**: 24 小时内回复

**报告内容应包含**:
- 漏洞描述和影响
- 复现步骤
- 攻击场景分析
- 修复建议 (可选)

**请不要**:
- 在公共 issue 中公开安全漏洞
- 进行破坏性测试
- 未经授权访问他人系统

---

## 📚 参考资料

- [OWASP API Security Top 10](https://owasp.org/www-project-api-security/)
- [Go 安全最佳实践](https://go.dev/security/)
- [CWE-200: Information Exposure](https://cwe.mitre.org/data/definitions/200.html)
- [CWE-918: Server-Side Request Forgery](https://cwe.mitre.org/data/definitions/918.html)
- [CWE-400: Resource Exhaustion](https://cwe.mitre.org/data/definitions/400.html)


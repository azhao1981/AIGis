# AIGis 安全配置示例

本文档提供各种安全场景的具体配置示例。

## 📌 目录

1. [快速安全加固 (5分钟)](#快速安全加固-5分钟)
2. [认证方案对比](#认证方案对比)
3. [生产环境配置模板](#生产环境配置模板)
4. [Nginx 反向代理配置](#nginx-反向代理配置)
5. [Docker 安全部署](#docker-安全部署)
6. [完整示例: 企业级部署](#完整示例-企业级部署)

---

## 快速安全加固 (5分钟)

### 方案 A: 环境变量认证 (最简单)

```bash
# 1. 设置环境变量
export AIGIS_API_KEY="sk-your-secret-key-here"
export OPENAI_API_KEY="sk-openai-key"

# 2. 修改配置文件
cat > .env << EOF
AIGIS_API_KEY=sk-your-secret-key-here
OPENAI_API_KEY=sk-openai-key
EOF

# 3. 添加简单的认证中间件
# (修改 internal/server/http.go，在 handleChatCompletions 开始处添加)
# 详细代码见下方 "API Key 认证实现"
```

### 方案 B: 使用 Nginx 代理 (推荐)

```nginx
# /etc/nginx/sites-available/aigis.conf
server {
    listen 443 ssl http2;
    server_name api.yourdomain.com;

    # SSL 证书
    ssl_certificate /etc/ssl/certs/yourdomain.crt;
    ssl_certificate_key /etc/ssl/private/yourdomain.key;

    # 安全加固
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;

    # 速率限制
    limit_req_zone $binary_remote_addr zone=aigis:10m rate=10r/s;

    location / {
        # API Key 认证
        auth_request /auth;

        # 速率限制
        limit_req zone=aigis burst=20 nodelay;

        # 转发到 AIGis
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        # 超时设置
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;

        # 请求体大小限制
        client_max_body_size 10M;
    }

    # 认证端点
    location = /auth {
        internal;
        proxy_pass http://127.0.0.1:8080/health;
        # 这里可以改为实际的认证逻辑
    }
}
```

---

## 认证方案对比

### 1. 简单 API Key (单用户/内部服务)

**代码实现 (`internal/server/http.go`)**:

```go
package server

import (
    "net/http"
    "os"
    "strings"
)

// APIKeyAuthProvider 管理 API Key 认证
type APIKeyAuthProvider struct {
    validKeys map[string]bool
}

func NewAPIKeyAuthProvider() *APIKeyAuthProvider {
    keys := make(map[string]bool)

    // 从环境变量加载
    if key := os.Getenv("AIGIS_API_KEY"); key != "" {
        keys[key] = true
    }

    // 支持多个 key
    if keysStr := os.Getenv("AIGIS_API_KEYS"); keysStr != "" {
        for _, key := range strings.Split(keysStr, ",") {
            key = strings.TrimSpace(key)
            if key != "" {
                keys[key] = true
            }
        }
    }

    return &APIKeyAuthProvider{validKeys: keys}
}

func (a *APIKeyAuthProvider) Authenticate(r *http.Request) bool {
    // 方式1: X-API-Key 头
    apiKey := r.Header.Get("X-API-Key")
    if apiKey != "" {
        return a.validKeys[apiKey]
    }

    // 方式2: Authorization: Bearer <key>
    authHeader := r.Header.Get("Authorization")
    if strings.HasPrefix(authHeader, "Bearer ") {
        apiKey = strings.TrimPrefix(authHeader, "Bearer ")
        return a.validKeys[apiKey]
    }

    // 方式3: Query 参数
    apiKey = r.URL.Query().Get("api_key")
    return a.validKeys[apiKey]
}

// 在 handleChatCompletions 中使用
func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    // 1. 验证 API Key
    authProvider := NewAPIKeyAuthProvider()
    if !authProvider.Authenticate(r) {
        s.logger.Warn("Unauthorized access attempt",
            zap.String("ip", r.RemoteAddr),
            zap.String("user_agent", r.UserAgent()),
        )
        http.Error(w, `{"error": "Unauthorized"}`, http.StatusUnauthorized)
        return
    }

    // 2. 原有逻辑...
    // ...
}
```

**环境变量配置**:
```bash
# 单个 Key
export AIGIS_API_KEY="sk-1234567890abcdef"

# 或多个 Keys (用逗号分隔)
export AIGIS_API_KEYS="sk-key1,sk-key2,sk-key3"
```

**客户端调用**:
```bash
# 方法1: Header
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "X-API-Key: sk-1234567890abcdef" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 方法2: Authorization Bearer
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-1234567890abcdef" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 方法3: Query 参数
curl -X POST http://localhost:8080/v1/chat/completions?api_key=sk-1234567890abcdef \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'
```

---

### 2. JWT 认证 (多用户/外部访问)

**依赖**: `go get github.com/golang-jwt/jwt/v5`

**代码实现**:

```go
package auth

import (
    "errors"
    "os"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

type JWTClaims struct {
    UserID    string `json:"user_id"`
    Email     string `json:"email"`
    Audience  string `json:"aud"`
    jwt.RegisteredClaims
}

type JWTAuthProvider struct {
    secret []byte
}

func NewJWTAuthProvider() *JWTAuthProvider {
    secret := os.Getenv("JWT_SECRET")
    if secret == "" {
        panic("JWT_SECRET environment variable is required")
    }
    return &JWTAuthProvider{secret: []byte(secret)}
}

func (j *JWTAuthProvider) GenerateToken(userID, email string) (string, error) {
    claims := &JWTClaims{
        UserID:    userID,
        Email:     email,
        Audience:  "aigis-api",
        RegisteredClaims: jwt.RegisteredClaims{
            Issuer:    "aigis",
            Subject:   userID,
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
            IssuedAt:  jwt.NewNumericDate(time.Now()),
            NotBefore: jwt.NewNumericDate(time.Now()),
        },
    }

    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString(j.secret)
}

func (j *JWTAuthProvider) ValidateToken(tokenString string) (*JWTClaims, error) {
    token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
        // Validate signing method
        if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, errors.New("unexpected signing method")
        }
        return j.secret, nil
    })

    if err != nil {
        return nil, err
    }

    if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
        return claims, nil
    }

    return nil, errors.New("invalid token")
}

// 在 HTTP handler 中使用
func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    jwtAuth := auth.NewJWTAuthProvider()

    // 提取 token
    authHeader := r.Header.Get("Authorization")
    if !strings.HasPrefix(authHeader, "Bearer ") {
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }
    tokenString := strings.TrimPrefix(authHeader, "Bearer ")

    // 验证
    claims, err := jwtAuth.ValidateToken(tokenString)
    if err != nil {
        http.Error(w, "Invalid token", http.StatusUnauthorized)
        return
    }

    // 记录用户 ID 用于审计日志
    ctx := core.NewGatewayContext(r.Context(), s.logger.Logger)
    ctx.UserID = claims.UserID

    // 后续处理...
}
```

**生成 Token 示例**:
```go
package main

import "aigis/internal/auth"

func main() {
    jwtAuth := auth.NewJWTAuthProvider()
    token, err := jwtAuth.GenerateToken("user123", "user@example.com")
    if err != nil {
        panic(err)
    }
    fmt.Println("Token:", token)
}
```

---

### 3. Upstream URL 验证 (防 SSRF)

**完整实现**:

```go
package security

import (
    "fmt"
    "net"
    "net/url"
)

var blockedIPRanges = []*net.IPNet{
    mustParseCIDR("127.0.0.0/8"),      // 本地回环
    mustParseCIDR("10.0.0.0/8"),       // 私有网络
    mustParseCIDR("172.16.0.0/12"),    // 私有网络
    mustParseCIDR("192.168.0.0/16"),   // 私有网络
    mustParseCIDR("169.254.0.0/16"),   // 链路本地
    mustParseCIDR("0.0.0.0/8"),        // 无效地址
    mustParseCIDR("100.64.0.0/10"),    // CGNAT
}

func mustParseCIDR(cidr string) *net.IPNet {
    _, ipnet, _ := net.ParseCIDR(cidr)
    return ipnet
}

// ValidateUpstreamURL 验证上游服务 URL 安全性
func ValidateUpstreamURL(rawURL string) error {
    // 1. 解析 URL
    u, err := url.Parse(rawURL)
    if err != nil {
        return fmt.Errorf("invalid URL format: %w", err)
    }

    // 2. 协议检查 (只允许 HTTPS)
    if u.Scheme != "https" {
        return fmt.Errorf("only HTTPS protocol allowed, got: %s", u.Scheme)
    }

    // 3. Host 检查 (必须有域名)
    if u.Hostname() == "" {
        return fmt.Errorf("URL must have a hostname")
    }

    // 4. DNS 解析和 IP 检查
    ips, err := net.LookupIP(u.Hostname())
    if err != nil {
        return fmt.Errorf("DNS lookup failed: %w", err)
    }

    if len(ips) == 0 {
        return fmt.Errorf("no IP addresses found for hostname: %s", u.Hostname())
    }

    // 5. 检查所有解析到的 IP
    for _, ip := range ips {
        // 跳过 IPv6 暂时 (如需支持需要额外处理)
        if ip.To4() == nil {
            continue
        }

        for _, blocked := range blockedIPRanges {
            if blocked.Contains(ip) {
                return fmt.Errorf("blocked IP address: %s (belongs to %s)", ip, blocked)
            }
        }
    }

    // 6. 验证端口 (可选: 限制为 443)
    port := u.Port()
    if port != "" && port != "443" {
        // 如需要严格限制普通 HTTPS 流量
        // return fmt.Errorf("only standard HTTPS port (443) allowed")
    }

    return nil
}

// 在 config 初始化时调用
func (c *EngineConfig) Validate() error {
    for _, route := range c.Routes {
        if err := ValidateUpstreamURL(route.Upstream.BaseURL); err != nil {
            return fmt.Errorf("route %s upstream validation failed: %w", route.ID, err)
        }
    }
    return nil
}
```

**在配置加载时验证**:

```go
// internal/config/config.go

func LoadEngineConfig() (*engine.EngineConfig, error) {
    var config engine.EngineConfig

    if err := viper.UnmarshalKey("engine", &config); err != nil {
        return nil, fmt.Errorf("failed to unmarshal engine config: %w", err)
    }

    // 安全验证
    if err := security.ValidateUpstreamConfig(&config); err != nil {
        return nil, fmt.Errorf("security validation failed: %w", err)
    }

    return &config, nil
}
```

---

## 生产环境配置模板

### 1. 基础生产配置 (`configs/production.yaml`)

```yaml
# AIGis 生产环境配置模板
# 使用前请仔细阅读 docs/security/SECURITY.md

# 服务器配置
server:
  # 监听本地，通过 Nginx 反向代理
  host: "127.0.0.1"
  port: 8080

# 日志配置
log:
  level: "warn"  # 生产环境使用 warn 或 error
  # 如需文件输出，确保文件权限正确
  # output: "/var/log/aigis/app.log"

# 认证配置
# 注意: API Key 必须通过环境变量设置
# AIGIS_API_KEY=sk-your-secret-key
auth:
  # 认证模式: "api_key", "jwt", "none" (不推荐)
  mode: "api_key"

  # 允许的 API Keys (从环境变量加载)
  api_keys_env: "AIGIS_API_KEYS"

# 引擎配置
engine:
  routes:
    # 生产环境只配置必要的上游服务
    - id: "openai-production"
      matcher:
        model: "^gpt-.*"
      upstream:
        # 必须是 HTTPS
        base_url: "https://api.openai.com/v1"
        path: "/chat/completions"
        auth_strategy: "bearer"
        token_env: "OPENAI_API_KEY"
      transforms:
        # 只使用 PII 脱敏，避免不安全的模板
        - type: "pii"
          config: {}

    # 其他路由建议注释掉，按需启用
    # - id: "claude"
    #   ...

    # 兜底路由 (谨慎使用)
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

### 2. 环境变量模板 (`.env.production.example`)

```bash
# === 服务器配置 ===
AIGIS_SERVER_HOST=127.0.0.1
AIGIS_SERVER_PORT=8080

# === 日志配置 ===
AIGIS_LOG_LEVEL=warn  # debug, info, warn, error

# === 认证密钥 (必须修改) ===
# 单个 API Key (简单场景)
AIGIS_API_KEY=sk-生产环境的密钥这里

# 或多个 API Keys (用逗号分隔)
AIGIS_API_KEYS=sk-key1,sk-key2,sk-key3

# === JWT 配置 (如果使用 JWT 模式) ===
# JWT_SECRET=your-256-bit-secret-key-min-32-chars

# === 上游服务密钥 ===
OPENAI_API_KEY=sk-openai-key
# DIFY_API_KEY=your-dify-key
# DASHSCOPE_API_KEY=your-dashscope-key
```

### 3. .env 处理最佳实践

```bash
# 1. 创建生产环境配置
cp .env.production.example .env.production

# 2. 设置文件权限 (只有所有者可读)
chmod 600 .env.production

# 3. 运行时指定环境
set -a; source .env.production; set +a
./bin/aigis serve --config configs/production.yaml

# 4. Docker 方式 (推荐)
docker run -d \
  --name aigis \
  --env-file .env.production \
  -p 127.0.0.1:8080:8080 \
  -v $(pwd)/configs/production.yaml:/app/configs/production.yaml \
  aigis:latest \
  --config /app/configs/production.yaml serve
```

---

## Nginx 反向代理配置

### 推荐配置 (`/etc/nginx/sites-available/aigis.conf`)

```nginx
# AIGis API 网关反向代理配置
# 安全加固版本

# 速率限制 - 每 IP 每秒 10 个请求，突发 20 个
limit_req_zone $binary_remote_addr zone=aigis_api:10m rate=10r/s;

# 连接数限制 - 每 IP 最多 50 个连接
limit_conn_zone $binary_remote_addr zone=aigis_conn:10m;

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;

    server_name api.yourdomain.com;

    # === SSL 配置 ===
    ssl_certificate /etc/ssl/certs/api.yourdomain.com.crt;
    ssl_certificate_key /etc/ssl/private/api.yourdomain.com.key;

    # 推荐 SSL 配置
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 10m;

    # OCSP Stapling
    ssl_stapling on;
    ssl_stapling_verify on;

    # 安全头
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    # 不暴露服务器信息
    server_tokens off;

    # === 请求限制 ===
    limit_req zone=aigis_api burst=20 nodelay;
    limit_conn aigis_conn 50;

    # === 客户端限制 ===
    # 最大请求体 10MB
    client_max_body_size 10M;
    client_body_buffer_size 1M;

    # 超时设置
    client_header_timeout 30s;
    client_body_timeout 60s;
    send_timeout 60s;
    keepalive_timeout 75s;

    # === 日志 ===
    access_log /var/log/nginx/aigis_access.log combined buffer=32k flush=1m;
    error_log /var/log/nginx/aigis_error.log warn;

    # === 代理配置 ===
    location / {
        # 代理到 AIGis (监听在本地 8080)
        proxy_pass http://127.0.0.1:8080;

        # 代理头设置
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # 超时
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;

        # 缓冲区
        proxy_buffering on;
        proxy_buffer_size 4k;
        proxy_buffers 8 4k;
        proxy_busy_buffers_size 8k;

        # 错误处理
        proxy_next_upstream error timeout http_500 http_502 http_503 http_504;
        proxy_next_upstream_tries 3;
    }

    # === 健康检查端点 ===
    location /health {
        access_log off;  # 减少日志噪音
        proxy_pass http://127.0.0.1:8080/health;
    }

    # === 拒绝其他路径 ===
    location ~ /\. {
        deny all;
        access_log off;
        return 404;
    }
}

# HTTP 重定向到 HTTPS
server {
    listen 80;
    listen [::]:80;
    server_name api.yourdomain.com;

    return 301 https://$server_name$request_uri;
}
```

### 启用配置

```bash
# 检查配置语法
sudo nginx -t

# 启用配置
sudo ln -s /etc/nginx/sites-available/aigis.conf /etc/nginx/sites-enabled/

# 重新加载 Nginx
sudo nginx -s reload
```

---

## Docker 安全部署

### 1. 安全 Dockerfile (`Dockerfile.secure`)

```dockerfile
# === 构建阶段 ===
FROM golang:1.25-alpine AS builder

# 安全编译参数
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    GOTOOLCHAIN=local

WORKDIR /app

# 复制依赖 (利用缓存)
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并构建
COPY . .
RUN go build -ldflags="-w -s" -o /app/bin/aigis ./cmd/aigis

# === 运行阶段 (最小化) ===
FROM gcr.io/distroless/static-debian12:nonroot

# 非 root 用户
USER nonroot:nonroot

# 工作目录
WORKDIR /app

# 复制二进制
COPY --from=builder --chown=nonroot:nonroot /app/bin/aigis /app/aigis
COPY --chown=nonroot:nonroot configs/config.yaml /app/configs/config.yaml

# 只暴露 8080
EXPOSE 8080

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/app/aigis", "health"] || exit 1

# 入口点
ENTRYPOINT ["/app/aigis"]
CMD ["serve"]
```

### 2. 安全运行脚本 (`scripts/run-secure.sh`)

```bash
#!/bin/bash
set -euo pipefail

# AIGis 安全运行脚本

# 检查必要环境变量
if [[ -z "${AIGIS_API_KEY:-}" ]]; then
    echo "错误: 必须设置 AIGIS_API_KEY 环境变量"
    exit 1
fi

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    echo "错误: 必须设置 OPENAI_API_KEY 环境变量"
    exit 1
fi

# 设置 umask 限制文件权限
umask 077

# 运行容器
docker run -d \
  --name aigis \
  --restart unless-stopped \
  --read-only \
  --tmpfs /tmp \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  --cap-add NET_BIND_SERVICE \
  -p 127.0.0.1:8080:8080 \
  --env AIGIS_API_KEY="${AIGIS_API_KEY}" \
  --env OPENAI_API_KEY="${OPENAI_API_KEY}" \
  --env AIGIS_LOG_LEVEL="${AIGIS_LOG_LEVEL:-warn}" \
  --env-file .env \
  aigis:secure-latest

echo "AIGis 已启动，监听在 127.0.0.1:8080"
```

### 3. Docker Compose (`docker-compose.secure.yml`)

```yaml
version: "3.8"

services:
  aigis:
    build:
      context: .
      dockerfile: Dockerfile.secure
    container_name: aigis
    restart: unless-stopped

    # 安全配置
    read_only: true
    tmpfs:
      - /tmp
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - NET_BIND_SERVICE

    # 端口绑定 (只监听本地)
    ports:
      - "127.0.0.1:8080:8080"

    # 环境变量 (从文件加载)
    env_file:
      - .env.production

    # 资源限制
    deploy:
      resources:
        limits:
          memory: 512M
        reservations:
          memory: 64M

    # 健康检查
    healthcheck:
      test: ["CMD", "/app/aigis", "health"]
      interval: 30s
      timeout: 3s
      retries: 3
      start_period: 10s

    # 日志配置
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"

  # Nginx 反向代理
  nginx:
    image: nginx:alpine
    container_name: aigis-nginx
    restart: unless-stopped

    ports:
      - "80:80"
      - "443:443"

    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf:ro
      - ./ssl:/etc/ssl:ro
      - ./logs/nginx:/var/log/nginx

    depends_on:
      - aigis

    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
```

---

## 完整示例: 企业级部署

### 架构图

```
┌─────────────────────────────────────────────────┐
│              外部网络                            │
└────────────────┬────────────────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────────┐
│  CloudFlare / WAF (可选)                         │
│  - DDoS 防护                                      │
│  - 威胁检测                                       │
└────────────────┬────────────────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────────┐
│  Nginx (反向代理 / 7层负载均衡)                   │
│  - SSL/TLS 终端                                   │
│  - 速率限流 (10 req/s per IP)                    │
│  - 连接限制 (50 concurrent)                      │
│  - 访问日志                                       │
└────────────────┬────────────────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────────┐
│  AIGis (API 网关)                                │
│  - 端口: 8080 (监听 127.0.0.1)                  │
│  - 认证: API Key / JWT                           │
│  - PII 脱敏                                       │
│  - 路由转发                                       │
└────────────────┬────────────────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────────┐
│  上游 LLM 服务                                   │
│  - OpenAI API                                    │
│  - Claude API                                    │
│  - 其他 LLM 服务                                 │
└─────────────────────────────────────────────────┘
```

### 配置清单

#### 1. 服务器准备

```bash
# 创建专用用户
sudo useradd -r -s /bin/false aigis

# 创建目录结构
sudo mkdir -p /opt/aigis/{bin,config,logs}
sudo chown -R aigis:aigis /opt/aigis
sudo chmod 750 /opt/aigis

# 安装依赖
sudo apt update
sudo apt install nginx docker.io
```

#### 2. 安全配置

```bash
# 生成自签名证书 (或使用 Let's Encrypt)
sudo openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout /etc/ssl/private/aigis.key \
  -out /etc/ssl/certs/aigis.crt \
  -subj "/CN=api.yourdomain.com"

# 设置权限
sudo chmod 600 /etc/ssl/private/aigis.key
sudo chmod 644 /etc/ssl/certs/aigis.crt
```

#### 3. 部署脚本 (`deploy.sh`)

```bash
#!/bin/bash
set -euo pipefail

# === 安全部署脚本 ===

# 检查运行用户
if [[ "$(whoami)" != "root" ]]; then
    echo "必须以 root 运行此脚本"
    exit 1
fi

# 配置路径
PROJECT_DIR="/opt/aigis"
CONFIG_DIR="${PROJECT_DIR}/config"
LOG_DIR="${PROJECT_DIR}/logs"

# 1. 从环境变量创建 .env
echo "=== 创建环境变量文件 ==="
cat > "${CONFIG_DIR}/.env.production" << EOF
# 安全配置
AIGIS_API_KEY=${AIGIS_API_KEY}
OPENAI_API_KEY=${OPENAI_API_KEY}

# 日志级别
AIGIS_LOG_LEVEL=warn

# 服务器配置
AIGIS_SERVER_HOST=127.0.0.1
AIGIS_SERVER_PORT=8080
EOF

chmod 600 "${CONFIG_DIR}/.env.production"
chown aigis:aigis "${CONFIG_DIR}/.env.production"

# 2. 部署配置文件
echo "=== 部署配置文件 ==="
cp configs/production.yaml "${CONFIG_DIR}/"
chown aigis:aigis "${CONFIG_DIR}/production.yaml"
chmod 640 "${CONFIG_DIR}/production.yaml"

# 3. Nginx 配置
echo "=== 配置 Nginx ==="
cp docs/security/examples/nginx.conf /etc/nginx/sites-available/aigis.conf
ln -sf /etc/nginx/sites-available/aigis.conf /etc/nginx/sites-enabled/
nginx -t && nginx -s reload

# 4. 运行容器
echo "=== 启动容器 ==="
cd "${PROJECT_DIR}"

# 拉取镜像 (如果有)
# docker pull registry.yourcompany.com/aigis:latest

# 停止旧容器
docker stop aigis 2>/dev/null || true
docker rm aigis 2>/dev/null || true

# 启动新容器
docker run -d \
  --name aigis \
  --restart unless-stopped \
  --read-only \
  --tmpfs /tmp \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  -p 127.0.0.1:8080:8080 \
  --env-file "${CONFIG_DIR}/.env.production" \
  -v "${CONFIG_DIR}/production.yaml:/app/configs/production.yaml:ro" \
  -v "${LOG_DIR}:/app/logs" \
  --user 1000:1000 \
  aigis:latest

echo "=== 部署完成 ==="
echo "服务状态:"
docker ps | grep aigis
echo ""
echo "日志查看: docker logs -f aigis"
echo "健康检查: curl https://api.yourdomain.com/health"
```

#### 4. 日志审计配置 (`rsyslog.d/aigis.conf`)

```conf
# /etc/rsyslog.d/aigis.conf

# 收集 AIGis 日志
if $programname == 'aigis' then {
    /var/log/aigis/app.log
    & stop
}

# 过滤敏感信息 (如果日志包含请求体)
if $msg contains 'api_key' then stop
if $msg contains 'Authorization' then stop
```

#### 5. 监控和告警

```bash
# 创建监控脚本 /opt/aigis/scripts/monitor.sh
#!/bin/bash

# 检查服务健康
HEALTH=$(curl -s https://api.yourdomain.com/health)

# 检查容器状态
if [[ "$HEALTH" != '{"status":"ok"}' ]]; then
    echo "AIGis 健康检查失败" | mail -s "AIGis 告警" admin@yourcompany.com
fi

# 检查日志中的异常
if docker logs aigis 2>&1 | grep -i "error\|unauthorized\|failed" | tail -10; then
    echo "发现异常日志" | mail -s "AIGis 异常" admin@yourcompany.com
fi
```

---

## 测试清单

### 安全测试

```bash
# 1. 无认证访问测试 (应该返回 401)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}'

# 2. 有效认证测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "X-API-Key: $AIGIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "PII test: test@example.com 13800138000"}]}'

# 3. 请求体大小测试
# 应该返回 413
python -c 'print("{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\""+"A"*12000000+"\"}]}")' \
  | curl -X POST http://localhost:8080/v1/chat/completions \
  -H "X-API-Key: $AIGIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d @- \
  -w "\nStatus: %{http_code}\n"

# 4. 健康检查
curl https://api.yourdomain.com/health
```

### 性能测试

```bash
# 安装 hey (https://github.com/rakyll/hey)
hey -n 1000 -c 10 -H "X-API-Key: $AIGIS_API_KEY" \
  -m POST -D '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' \
  http://localhost:8080/v1/chat/completions
```

---

做完这些配置后，请再次阅读 [SECURITY.md](SECURITY.md) 中的检查清单，确保所有 P0 和 P1 项都已完成。

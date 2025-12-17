# AIGis 生产环境部署安全检查清单

## 🚨 生产部署前必须完成 (P0/P1)

### 认证和授权
- [ ] `SECURITY.md` 3.1 节: 实现 API Key 或 JWT 认证
  - 修改 `internal/server/http.go` 的 `handleChatCompletions` 函数
  - 测试: 无认证请求应返回 401
- [ ] 设置 `AIGIS_API_KEY` 环境变量
  ```bash
  export AIGIS_API_KEY="sk-your-256-bit-secret"
  ```
- [ ] 确保认证密钥足够复杂 (推荐 32+ 字符)

### 防 SSRF
- [ ] `SECURITY.md` 3.2 节: 验证 upstream URL
  - 实现 `ValidateUpstreamURL` 函数
  - 只允许 HTTPS
  - 阻止私有 IP: 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, 169.254.0.0/16
- [ ] 在 `config.LoadEngineConfig()` 中调用验证函数

### 请求限制
- [ ] `SECURITY.md` 3.3 节: 限制请求体大小
  ```go
  maxBytes := int64(10 * 1024 * 1024)  // 10MB
  r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
  ```
- [ ] 测试: 发送 >10MB 请求应返回 413

### 日志安全
- [ ] 确认日志级别为 `info` 或 `warn` (非 `debug`)
- [ ] 确认日志不包含敏感数据 (API Key, 请求内容)
- [ ] 如果写入文件，权限设置为 600

## 🔒 网络安全 (P1)

### Nginx 配置
- [ ] 配置 Nginx 反向代理
  - 监听 127.0.0.1:8080 (不对外直接暴露)
  - 通过 Nginx 端口 443 对外服务
- [ ] SSL/TLS 配置完成
  - 使用 TLS 1.2/1.3
  - 正确配置证书
- [ ] 速率限制配置
  ```nginx
  limit_req_zone $binary_remote_addr zone=aigis:10m rate=10r/s;
  limit_req zone=aigis burst=20 nodelay;
  ```
- [ ] 请求体大小限制
  ```nginx
  client_max_body_size 10M;
  ```

### 防火墙
- [ ] 服务器防火墙只开放必要端口
  - 443 (HTTPS)
  - 22 (SSH, 如果需要)
- [ ] 确认 8080 端口只监听本地 (127.0.0.1)

## 🔐 环境和配置 (P1)

### 配置文件
- [ ] `configs/production.yaml` 创建并验证
  - `server.host: "127.0.0.1"`
  - `log.level: "warn"`
- [ ] `.env.production` 创建并设置权限
  ```bash
  chmod 600 .env.production
  ```
- [ ] `.env.production` 不提交到 Git
- [ ] 从 `.gitignore` 移除 `CLAUDE.md`

### API Key 管理
- [ ] 所有 API Key 通过环境变量设置
  - `AIGIS_API_KEY`
  - `OPENAI_API_KEY`
  - 以及任何其他上游服务的 Key
- [ ] 配置文件中不出现任何 API Key 示例

## 🛡️ 系统安全 (P2)

### 文件权限
- [ ] 项目目录权限为 750 或更严格
  ```bash
  chmod 750 /path/to/aigis
  ```
- [ ] 配置文件权限为 640
- [ ] 日志文件权限为 600 (如果写入文件)
- [ ] 执行文件权限为 755

### 用户权限
- [ ] 推荐使用专用用户运行
  ```bash
  sudo useradd -r -s /bin/false aigis
  sudo chown -R aigis:aigis /opt/aigis
  ```

### Docker 安全 (如果使用)
- [ ] 非 root 用户运行
  ```dockerfile
  USER nonroot:nonroot
  ```
- [ ] 只读文件系统
  ```bash
  --read-only --tmpfs /tmp
  ```
- [ ] 限制能力
  ```bash
  --cap-drop ALL --cap-add NET_BIND_SERVICE
  ```
- [ ] 禁止权限提升
  ```bash
  --security-opt no-new-privileges:true
  ```

## 📊 监控和审计 (P2)

### 日志监控
- [ ] 配置日志收集 (如 ELK, Vector)
- [ ] 设置异常告警
  - 401/403 错误激增
  - 5xx 错误
  - 请求大小异常
  - 访问非标准路径

### 健康检查
- [ ] `/health` 端点可用
- [ ] 设置监控告警
  ```bash
  curl -f https://api.yourdomain.com/health || echo "AIGis 健康检查失败"
  ```

## 🧪 测试

### 安全测试
```bash
# 1. 认证测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'
# ✅ 预期: 401 Unauthorized

# 2. 大小限制测试
python -c 'print("A"*11000000)' | curl -X POST http://localhost:8080/v1/chat/completions \
  -H "X-API-Key: $AIGIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d @- -w "%{http_code}"
# ✅ 预期: 413

# 3. PII 脱敏测试
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "X-API-Key: $AIGIS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"email: test@example.com phone: 13800138000"}]}'
# ✅ 验证日志中 PII 被脱敏
```

### 性能基准
```bash
# 安装 hey: go install github.com/rakyll/hey@latest
hey -n 100 -c 5 \
  -H "X-API-Key: $AIGIS_API_KEY" \
  -m POST -D '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}' \
  http://localhost:8080/v1/chat/completions
```

## 📝 配置验证

### 环境变量检查清单
```bash
# 必须设置的环境变量
echo "AIGIS_API_KEY: ${AIGIS_API_KEY:-NOT SET}"
echo "OPENAI_API_KEY: ${OPENAI_API_KEY:-NOT SET}"
echo "AIGIS_LOG_LEVEL: ${AIGIS_LOG_LEVEL:-info}"
```

### 配置文件验证
```bash
# 检查 production.yaml
cat configs/production.yaml | grep "host\|level\|base_url"

# 预期输出:
# host: "127.0.0.1"
# level: "warn"
# base_url: "https://api.openai.com/v1"  # 只有 HTTPS
```

### 网络监听验证
```bash
# 确认只监听本地
netstat -tlnp | grep 8080
# 预期: 127.0.0.1:8080 (不是 0.0.0.0:8080)
```

## 🔍 最终部署检查

### 部署前确认
- [ ] 已阅读并理解 `SECURITY.md` 全文
- [ ] 已完成所有 P0 项 (认证、防 SSRF、大小限制)
- [ ] 已完成所有 P1 项
- [ ] 已在测试环境验证所有安全功能
- [ ] 已配置生产环境日志
- [ ] 已设置监控和告警
- [ ] 已准备回滚方案

### 部署后验证
- [ ] 服务启动成功
- [ ] 健康检查通过
- [ ] 认证正常工作
- [ ] 日志输出正常
- [ ] 上游服务可正常访问
- [ ] 无错误日志

### 文档更新
- [ ] 更新 `CHANGELOG.md`
- [ ] 记录安全配置变更
- [ ] 更新服务器文档

---

## 🆘 紧急情况处理

### 如果发现漏洞被利用

1. **立即执行**:
   ```bash
   # 1. 隔离服务
   docker stop aigis

   # 2. 更改所有 API Key
   export AIGIS_API_KEY="sk-new-key"
   export OPENAI_API_KEY="sk-new-openai-key"

   # 3. 检查日志中是否有未授权访问
   docker logs aigis --since 1h | grep "unauthorized\|error"
   ```

2. **修复漏洞**

3. **重新部署**

4. **发布公告** (如果有用户影响)

### 紧急联系

- **漏洞报告**: 请参考 `SECURITY.md` 中的漏洞报告流程
- **代码审查**: 检查 `internal/server/http.go` 和 `internal/core/providers/universal.go`

---

## 📖 相关文档

- [完整安全文档](SECURITY.md) - 详细风险分析和修复方案
- [配置示例](SECURITY-EXAMPLES.md) - 生产环境配置模板
- [代码位置参考](#代码位置参考)

---

## 快速参考

### 关键代码位置
```
internal/server/http.go:141-224  - 主处理函数
internal/server/http.go:153       - 请求体读取
internal/core/providers/universal.go:232-292  - 上游请求
internal/core/providers/universal.go:199-229  - 模板变换
internal/config/config.go:16-36   - .env 加载
```

### 常用命令
```bash
# 环境变量设置
export AIGIS_API_KEY="sk-your-key"
export AIGIS_LOG_LEVEL="warn"

# 运行服务
./bin/aigis --config configs/production.yaml serve

# 查看日志
docker logs -f aigis

# 健康检查
curl https://api.yourdomain.com/health
```

---

**最后更新**: 2025-12-17
**版本**: 1.0
**状态**: ✅ 生产部署可用

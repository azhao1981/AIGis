# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

AIGis 是一个用 Go 语言开发的 AI 安全网关，提供对 AI/LLM 服务的访问控制和数据处理。

## 核心架构

### 模块化设计

项目采用模块化架构，核心组件包括：

- **Provider 接口** ([`internal/core/provider.go`](internal/core/provider.go)): LLM 提供商适配器接口，定义了统一的模型请求和响应处理
- **Processor 接口** ([`internal/core/processor.go`](internal/core/processor.go)): 中间件处理接口，支持请求/响应的拦截和修改
- **Pipeline** ([`internal/core/pipeline.go`](internal/core/pipeline.go)): 处理器管道，按优先级执行多个处理器
- **AIGisContext** ([`internal/core/context.go`](internal/core/context.go)): 扩展的上下文，支持线程安全的元数据存储

### 处理器实现

- **PII Guard** ([`internal/core/processors/pii_guard.go`](internal/core/processors/pii_guard.go)): 个人身份信息检测和脱敏处理器

### 服务层

- **HTTP Server** ([`internal/server/server.go`](internal/server/server.go)): 支持 graceful shutdown 的 HTTP 服务器

## 常用命令

### 构建和运行

```bash
# 构建项目
make build

# 运行服务（默认端口 8080）
make run

# 指定端口运行
make run-port PORT=3000

# 快速重启（先停止再启动）
./run.sh

# 停止服务
make kill
```

### 开发和测试

```bash
# 开发模式（需要 air，仅 Go 1.25+）
make dev

# Go 1.23/1.24 用户手动热重载
go run ./cmd/aigis serve

# 运行测试
make test

# 生成测试覆盖率报告
make test-coverage

# 代码格式化
make fmt

# 代码检查
make lint
```

### 依赖管理

```bash
# 安装依赖
make deps

# 更新依赖
make update-deps

# 安装开发工具
make install-tools
```

## 配置管理

配置优先级：环境变量 (AIGIS_*) > 命令行参数 > config.yaml

- 默认配置文件：[`configs/config.yaml`](configs/config.yaml)
- 支持自定义配置文件：`./bin/aigis --config /path/to/config.yaml serve`
- 环境变量示例：`AIGIS_SERVER_PORT=9000 ./bin/aigis serve`

## 服务接口

当前提供的基础接口：

- `/health`: 健康检查端点
- `/`: 根端点，返回服务状态

测试示例请参考：[`tests/test_base.sh`](tests/test_base.sh)

## Go 版本要求

- 推荐：Go 1.25
- 支持：Go 1.23、1.24、1.25
- 当前项目使用：Go 1.25.4（见 [`.go-version`](.go-version)）

## 测试 PII 处理器

```bash
# 启动服务后
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {
        "role": "user",
        "content": "My email is dangerous@coder.com and my phone is 13800138000. Can you optimize this SQL?"
      }
    ]
  }'
```
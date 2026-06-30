# AIGis

[English](README.md) | 简体中文

## AIGis 是什么

AIGis 是一个用 Go 语言开发的 AI 安全网关,为 AI/LLM 服务提供访问控制和数据处理。

## 使用方式

### 启动服务 (默认 0.0.0.0:8080)

```bash
./bin/aigis serve
```

### 指定端口

```bash
./bin/aigis serve -p 3000
```

### 使用环境变量

```bash
AIGIS_SERVER_PORT=9000 ./bin/aigis serve
```

### 使用自定义配置文件

```bash
./bin/aigis --config /path/to/config.yaml serve
```

配置优先级:

```
环境变量 (AIGIS_*) > 命令行参数 > config.yaml
```

## 项目结构

```bash
  aigis/
  ├── bin/aigis               # 编译产物
  ├── cmd/aigis/
  │   ├── main.go              # 入口
  │   ├── root.go              # Cobra 根命令 + Viper 配置
  │   └── serve.go             # serve 子命令
  ├── internal/
  │   ├── core/
  │   │   ├── context.go       # GatewayContext (线程安全 metadata)
  │   │   ├── provider.go      # Provider 接口 (LLM 适配器)
  │   │   └── processor.go     # Processor 接口 (中间件)
  │   └── server/
  │       └── server.go        # HTTP 服务器 (graceful shutdown)
  ├── configs/
  │   └── config.yaml          # 默认配置
  ├── go.mod
  └── go.sum
```

## 测试

```bash
go test -v ./tests/...
```

## 许可 (License)

本项目采用 **GNU AGPLv3** 协议开源,详见 [`LICENSE`](LICENSE)。

个人、研究及内部评估可在 AGPLv3 条款下免费使用。若需在闭源 / SaaS 等场景中使用且不愿承担 AGPLv3 的源码开放义务,请参阅 [`COMMERCIAL.md`](COMMERCIAL.md) 获取商业授权。

向本项目贡献代码前,请阅读并同意 [`CLA.md`](CLA.md)。

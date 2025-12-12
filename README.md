
## what is aigis


## 使用方式

### 启动服务 (默认 0.0.0.0:8080)
  ./bin/aigis serve

### 指定端口
  ./bin/aigis serve -p 3000

### 使用环境变量
  AIGIS_SERVER_PORT=9000 ./bin/aigis serve

### 使用自定义配置文件
  ./bin/aigis --config /path/to/config.yaml serve

  配置优先级

  环境变量 (AIGIS_*) > 命令行参数 > config.yaml

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

## test

```bash
go test -v ./tests/...
```

# AIGis

English | [简体中文](README.zh-CN.md)

## What is AIGis

AIGis is an AI security gateway written in Go that provides access control and data processing for AI/LLM services.

## Usage

### Start the server (default 0.0.0.0:8080)

```bash
./bin/aigis serve
```

### Specify a port

```bash
./bin/aigis serve -p 3000
```

### Use environment variables

```bash
AIGIS_SERVER_PORT=9000 ./bin/aigis serve
```

### Use a custom config file

```bash
./bin/aigis --config /path/to/config.yaml serve
```

Configuration precedence:

```
environment variables (AIGIS_*) > command-line flags > config.yaml
```

## Project Structure

```bash
  aigis/
  ├── bin/aigis               # build artifact
  ├── cmd/aigis/
  │   ├── main.go              # entry point
  │   ├── root.go              # Cobra root command + Viper config
  │   └── serve.go             # serve subcommand
  ├── internal/
  │   ├── core/
  │   │   ├── context.go       # GatewayContext (thread-safe metadata)
  │   │   ├── provider.go      # Provider interface (LLM adapter)
  │   │   └── processor.go     # Processor interface (middleware)
  │   └── server/
  │       └── server.go        # HTTP server (graceful shutdown)
  ├── configs/
  │   └── config.yaml          # default config
  ├── go.mod
  └── go.sum
```

## Test

```bash
go test -v ./tests/...
```

## License

This project is open-sourced under the **GNU AGPLv3** license, see [`LICENSE`](LICENSE).

Personal, research, and internal-evaluation use is free under the AGPLv3 terms. If you intend to use AIGis in closed-source / SaaS scenarios without complying with the AGPLv3 source-disclosure obligations, please see [`COMMERCIAL.md`](COMMERCIAL.md) for a commercial license.

Before contributing, please read and agree to the [`CLA.md`](CLA.md).

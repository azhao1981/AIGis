# Contributing to AIGis / 贡献指南

English | 简体中文 (below)

Thanks for your interest in contributing to AIGis!

## Contributor License Agreement (CLA)

AIGis is dual-licensed (AGPLv3 + commercial). Before your contribution can be merged, you must read and agree to the [`CLA.md`](CLA.md). Submitting a Pull Request is considered acceptance of the CLA.

## Development

```bash
make build        # build the binary
make test         # run tests
make fmt          # format code
make lint         # run linters
make run          # build and run (default :8080)
```

Go 1.25 is recommended (see [`.go-version`](.go-version)).

## Pull Request Checklist

Before opening a PR, make sure:

- [ ] `go build ./...` passes
- [ ] `go vet ./...` passes
- [ ] `go test ./...` passes
- [ ] Code is formatted (`make fmt`)
- [ ] Commit messages follow the existing style (e.g. `feat: ...`, `fix: ...`, `docs: ...`, `ci: ...`)

CI (GitHub Actions) runs build / vet / test on every push and PR to `main`.

## Reporting Issues

Please include: AIGis version / commit, Go version, config (with secrets redacted), and steps to reproduce.

---

# 贡献指南（简体中文）

感谢你有兴趣为 AIGis 做贡献!

## 贡献者授权协议 (CLA)

AIGis 采用双授权(AGPLv3 + 商业)。你的贡献被合并前,必须阅读并同意 [`CLA.md`](CLA.md)。提交 Pull Request 即视为接受 CLA。

## 开发

```bash
make build        # 构建二进制
make test         # 运行测试
make fmt          # 格式化代码
make lint         # 代码检查
make run          # 构建并运行 (默认 :8080)
```

推荐使用 Go 1.25(见 [`.go-version`](.go-version))。

## PR 提交清单

提交 PR 前,请确认:

- [ ] `go build ./...` 通过
- [ ] `go vet ./...` 通过
- [ ] `go test ./...` 通过
- [ ] 代码已格式化(`make fmt`)
- [ ] 提交信息遵循现有风格(如 `feat: ...`、`fix: ...`、`docs: ...`、`ci: ...`)

CI(GitHub Actions)会在每次 push 和向 `main` 的 PR 上运行 build / vet / test。

## 提交 Issue

请附上: AIGis 版本 / commit、Go 版本、配置(隐去敏感信息)及复现步骤。

# TODO — 待办

> 已完成项见 [`DONE.md`](DONE.md)。

## A. 工程门面 / 开源标配（非功能，低风险）
- [x] **LICENSE** — AGPLv3 双授权（LICENSE + COMMERCIAL.md + CLA.md）
- [x] **README** — 补充项目介绍 + 拆分中英双语（README.md / README.zh-CN.md）
- [x] **CI/CD** — 已加 `.github/workflows/ci.yml`（build + vet + test）
- [x] **CONTRIBUTING.md** — 已加中英双语贡献指南（CLA + PR 清单 + 提交规范）

## B. 功能方向（需确认是否要做 / 优先级）
- [ ] **更多 provider 适配** — 现有 openai / gemini / claude(glm) / dify；可继续加（需原生格式的则参考 dify 翻译那套）
- [x] **脱敏增强（进阶）** — per-route 额外自定义规则（transform `custom_rules`，进程级编译缓存，不污染共享 scanner）
- [x] **流式强审核** — 路由 `force_block`：stream 请求内部降级为 blocking，脱敏后做 egress 泄露复检（内置规则漏网即拒发），客户端仍收到伪流式 SSE

## 待澄清 / 观察项
- [x] 日志 `upstream` 字段改为打印解析后 URL —— 抽 `Upstream.ResolvedBaseURL()` 统一 env: 解析，日志与请求共用同一来源

# TODO — 待办

> 已完成项见 [`DONE.md`](DONE.md)。

## A. 工程门面 / 开源标配（非功能，低风险）
- [ ] **LICENSE** — 缺失，建议 MIT；无协议他人无法合法使用
- [ ] **README** — "what is aigis" 章节为空，缺项目介绍 / 功能说明 / 架构图 / 安装方式
- [ ] **CI/CD** — 无 `.github/workflows/`，建议加 `go build` + `go test` 自动化
- [ ] **CONTRIBUTING.md** — 缺贡献指南

## B. 功能方向（需确认是否要做 / 优先级）
- [ ] **限流 / 熔断** — 目前并发监控只统计不限流（当初按 YAGNI 留口）
- [ ] **响应缓存** — 相同请求短期缓存
- [ ] **更多 provider 适配** — 现有 openai / gemini / claude(glm) / dify；可继续加（需原生格式的则参考 dify 翻译那套）
- [ ] **脱敏增强** — `custom_rules` 已支持；可扩展：按 tag 选规则、按路由覆盖规则集
- [ ] **dify `message_replace` 流式处理** — 当前丢弃（输出审核整体替换场景）；OpenAI 流式无法回撤已发 delta，需设计权衡

## 待澄清 / 观察项
- [ ] 日志 `upstream` 字段打印的是配置原值（如 `env:AIGIS_DIFY_BASE_URL`）而非解析后 URL —— 纯显示问题，不影响请求；要不要改成打印解析后 URL

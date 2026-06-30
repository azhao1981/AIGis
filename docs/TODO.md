# TODO — 待办

> 已完成项见 [`DONE.md`](DONE.md)。

## A. 工程门面 / 开源标配（非功能，低风险）
- [x] **LICENSE** — AGPLv3 双授权（LICENSE + COMMERCIAL.md + CLA.md）
- [x] **README** — 补充项目介绍 + 拆分中英双语（README.md / README.zh-CN.md）
- [x] **CI/CD** — 已加 `.github/workflows/ci.yml`（build + vet + test）
- [x] **CONTRIBUTING.md** — 已加中英双语贡献指南（CLA + PR 清单 + 提交规范）

## B. 功能方向（需确认是否要做 / 优先级）
- [ ] **更多 provider 适配** — 现有 openai / gemini / claude(glm) / dify；可继续加（需原生格式的则参考 dify 翻译那套）
- [ ] **脱敏增强（进阶）** — 已支持 custom_rules + 按路由选规则子集；可再扩展 per-route 额外自定义规则
- [ ] **流式强审核** — dify message_replace 已 surface 替换内容，但流式无法回撤已发 delta；强审核场景的 blocking 自动降级可考虑

## 待澄清 / 观察项
- [ ] 日志 `upstream` 字段打印的是配置原值（如 `env:AIGIS_DIFY_BASE_URL`）而非解析后 URL —— 纯显示问题，不影响请求；要不要改成打印解析后 URL

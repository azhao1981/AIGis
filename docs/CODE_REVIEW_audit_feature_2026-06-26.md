# 代码审查：敏感信息审计追踪（audit）功能

| 项 | 内容 |
|---|---|
| 审查日期 | 2026-06-26 |
| 审查范围 | 工作区未提交改动（非 PR）：`git status` 中的 5 个已改文件 + 新增 `internal/core/audit/` 包 |
| 审查方法 | 应用 `code-review` skill + `karpathy-rules12` 行为准则；契约核对 + 编译/vet/测试实测 |
| 评分 | 🟡 **需修改**（1 项设计权衡需决策 + 2 项应改 + 4 项可选） |

> **更新 2026-06-26（preview 特性落地 + 评审处置后）**：本文初稿写于 `Preview` 特性之前，文中 `Record.Placeholders` / "metadata only, no plaintext" 等表述对应旧代码。当前实现为 `Record.Items []{type, placeholder, preview}`，preview 为部分打码（如 `te***om`），故审计文件含「部分明文」。处置结论见各条目末尾 **【处置】** 标注。已改：P2a/P2b/P3#1/P3#2/P3#4；驳回：P3#3；P1 维持（保留 placeholder，靠权限+文档兜底）。

---

## 1. 概述

本次改动新增**敏感信息审计追踪**：gateway 在 `Scanner.Mask` 屏蔽密钥时，向 `./logs/audit.jsonl` 追加**一条 metadata-only 的 JSONL**（规则类型 + 计数 + placeholder，**不含明文**）；干净请求不落盘。共 86 行，跨 5 个既有文件 + 1 个新包。

整体品味不错：外科手术式改动、无无关格式扰动、测试显式断言「明文不泄漏」、disabled 时连文件都不创建。但有一处**威胁模型权衡**必须由决策者拍板，另有几处可改进项。

## 2. 改动清单

| 文件 | 改动 |
|---|---|
| `configs/config.yaml` | 新增 `audit.enabled: true` 配置块 |
| `internal/config/config.go` | 新增 `AuditEnabled()`，缺省默认 true |
| `internal/core/context.go` | `AIGisContext` 增加 `detections []Detection` 字段 + `RecordDetection/Detections` 方法（复用 `vaultMu`） |
| `internal/core/security/scanner.go` | `Mask` 内追加对 `auditContext` 接口的断言，记录检测 |
| `internal/server/http.go` | 持有 `auditor`，`NewHTTPServer` 初始化、`Start` 关闭、`handleGateway` 用 `defer` 在请求末尾写审计 |
| `internal/core/audit/audit.go` | **新包**：`Auditor` / `Record`，JSONL 追加写，并发安全 |
| `internal/core/audit/audit_test.go` | **新**：3 个用例（正常落盘/干净请求/disabled no-op） |

## 3. 验证记录（实测，非臆测）

| 验证项 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | ✅ 零输出（干净） |
| 静态检查 | `go vet ./internal/core/audit/ ./internal/core/ ./internal/server/ ./internal/config/` | ✅ 零告警 |
| 单元测试 | `go test ./internal/core/audit/` | ✅ `ok (cached)` —— 源码未变，缓存有效，等同真跑过且全绿 |
| git 跟踪 | `git check-ignore -v logs/audit.jsonl` | ✅ 命中 `.gitignore:7 logs`，审计文件**不会**被提交 |
| 依赖契约 | 核对 `context.go` / `scanner.go` | ✅ `StartTime`、`SetMetadata/GetMetadata`、`rule.Name`、`NewGatewayContext` 均真实存在且签名匹配 |
| 循环 import | `audit → core` 单向；`scanner` 用 interface 断言避开 `core` | ✅ 无环，与既有 `vaultContext` 模式一致 |

**未跑**：`http.go` / `context.go` / `scanner.go` 的既有测试。本次为审查而非 verify，按规矩只跑与改动直接相关的单个包（audit）。需要可单独补跑。

**审计覆盖路径核对**：`defer s.auditor.Record(ctx)` 注册于 `http.go:203`，在 `handleGateway` return 时执行。所有 early-return（pipeline/route/provider 错误）下时序正确——**只要发生过 mask 就记录**；streaming（`SendStream`）与 blocking（`Send`）双路径均覆盖，因 `Mask` 发生在 `ExecuteRequest`（`http.go:206`），早于路径分支。

## 4. 问题清单

### 🔴 P1 — `Placeholders` 落盘 = 密钥的稳定哈希指纹（需决策，非 bug）

**位置**：`internal/core/audit/audit.go:27,73`（`Record.Placeholders` 字段）+ `internal/core/security/scanner.go:96-99`（`generatePlaceholder`）。

`generatePlaceholder` 是**确定性**的：

```go
hash := sha256.Sum256([]byte(original))
hashHex := hex.EncodeToString(hash[:])[:12]  // SHA256 前 12 hex = 48 bit
return fmt.Sprintf("__AIGIS_SEC_%s__", hashHex)
```

即同一明文密钥**永远**映射到同一 placeholder。`Record` 把这些 placeholder 原样写入磁盘 `audit.jsonl`。后果：

- 审计日志暴露「**哪些密钥**被用过、**出现频率**、去重后**数量**」——这是元数据泄露（不是明文，但仍是信号）。
- 48-bit 前缀哈希在已知 key 前缀（如 `sk-`）+ 算力下，理论上可暴力/彩虹表反推。

注释里 "metadata only, no plaintext" **字面成立**，但 placeholder 携带的是密钥的**部分哈希**。是否接受取决于威胁模型：

- **若 `audit.jsonl` 的存储/访问控制足够强** → 可接受，但建议把这个权衡**写进注释与 `config.yaml` 说明**。
- **若要更稳** → `by_type` 计数已足够做合规审计，`placeholders` 列表可去掉；或对 placeholder 再做一次不可逆哈希/截断后存。

> 这是设计决策不是代码缺陷，故未擅自改。**待决策者定方向。**
>
> **【处置：维持，保留 placeholder】** preview 特性落地后，审计已主动存储部分明文预览（更直接的信号），placeholder 哈希的边际泄露被消解；且 placeholder 是审计↔vault↔响应的关联键，删除即失溯源。缓解改走文件权限（见 P3#4 已收紧 0o600）+ 包注释已写明该文件敏感。

### 🟡 P2 — 错误被静默吞掉（违反 fail-loud，应改）

**位置**：`internal/core/audit/audit.go:96-103`。

```go
line, err := json.Marshal(rec)
if err != nil {
    return // metadata-only struct; marshal failure is not worth crashing the request
}
a.mu.Lock()
defer a.mu.Unlock()
a.f.Write(append(line, '\n'))  // (n, err) 被丢弃
```

- `a.f.Write(...)` 丢弃返回的 `(n, err)`：磁盘满 / 权限错 / 设备异常时**零信号**。
- `json.Marshal` 失败直接 `return`，连 stderr 都不打。

「不崩主请求」的判断是对的，但**不崩 ≠ 不留痕**。建议给 `Auditor` 注入一个 logger（或退而用 `fmt.Fprintf(os.Stderr, ...)`），把写入失败打出来，否则审计静默失效时无人知晓。

> **【处置：已改】** `Auditor` 注入 `*zap.Logger`（`New` 第三参，server 传入 `zapLogger`）。`json.Marshal` 与 `a.f.Write` 失败均 `log.Error(...)` 带 request_id；不崩主请求但响亮留痕。logger 可为 nil，每处使用前判空。

### 🟡 P2 — `TestRecord_CleanRequestWritesNothing` 断言过松（应改）

**位置**：`internal/core/audit/audit_test.go:106-110`。

```go
if _, err := os.Stat(path); err == nil {
    if lines := readLines(t, path); len(lines) != 0 {
        t.Fatalf("clean request should write no line, got %d", len(lines))
    }
}
```

`New(path, true)` 已用 `O_CREATE` 建文件，所以 `os.Stat` 的 `err == nil` 分支**必进**，外层 `if` 是死防御。它实际验证的是「clean 请求没追加行」——结论对，但写法误导。建议去掉外层 `if`，直接 `readLines` 判 `len == 0`，语义更清晰且断言更严格。

> **【处置：已改】** 去掉外层 `os.Stat` 死防御，直接 `readLines(path)` 断言 `len == 0`。

### 🟢 P3 — 轻微 / 可选

| # | 位置 | 说明 | 处置 |
|---|---|---|---|
| 1 | `internal/server/http.go:80,85` | `config.AuditEnabled()` 连续调两次（每次都查 viper）。可存入局部变量。 | ✅ **已改**：存 `auditEnabled` 局部变量。 |
| 2 | `internal/core/audit/audit.go:79,83` | 调了两次 `time.Now()`（`Timestamp` 与 `DurationMS` 各一）。可合并为一个 `now` 变量。 | ✅ **已改**：合并为单个 `now`，消除两次取时的微小偏移。 |
| 3 | `internal/core/audit/audit.go:26` | `ByType map[string]int` 的 JSON key 序列化顺序不稳定。解析端按 key 读无影响，但会让 `audit.jsonl` 的 diff/grep 不友好。 | ❌ **驳回（评审有误）**：Go `encoding/json` 对 `map[string]X` 的 key **按字典序排序**输出，`by_type` 实为确定性，无需改。 |
| 4 | `internal/core/audit/audit.go:49` | 审计文件权限 `0o644`（group/other 可读）。含密钥指纹，建议 `0o600`（owner only）。**注意**：这与 logger（`aigis.log` 同为 `0o644`）一致，属项目惯例——若统一收紧应 logger 一起改。 | ✅ **已改（仅 audit）**：收紧至 `0o600`。preview 落地后该文件含部分明文，比 `aigis.log` 敏感，故单独收紧，logger 维持 `0o644`。 |

## 5. 亮点（做得好的地方）

- **外科手术式改动**：86 行，无无关格式/命名扰动，每行改动都能追溯到需求（符合 karpathy rule 3）。
- **设计干净**：`detections` 与 `secretVault` 复用 `vaultMu`，注释清晰说明 why；`Detection` 结构体显式声明「Intentionally carries NO plaintext」。
- **测试验证意图**（rule 9）：`TestRecord_WritesMetadataOnly` 不仅查行为，还**显式断言明文密钥不出现在输出行**——这是「为什么重要」层面的测试。
- **默认安全**：`audit.enabled` 缺省默认 true；disabled 时连文件都不创建（`TestRecord_DisabledIsNoop` 覆盖）。
- **接口断言避免循环 import**：`scanner.go` 用 `auditContext` interface 断言，与既有 `vaultContext` 模式完全一致（rule 11 conformance）。
- **单 defer 覆盖双路径**：streaming + blocking 共用一个 `defer Record`，无重复逻辑。

## 6. 结论与待办

**结论**：功能正确、测试到位、改动克制，**可合并**。

**处置完成（2026-06-26）**：
- ✅ P2a 错误留痕：`Auditor` 注入 logger，marshal/write 失败 `log.Error`。
- ✅ P2b 测试断言：去死防御，直接断言 clean 请求 0 行。
- ✅ P3#1/#2/#4：去重 `AuditEnabled()`、合并 `now`、权限 `0o600`。
- ❌ P3#3：驳回（Go map key 序列化本即有序）。
- ➖ P1：维持现状，保留 placeholder（关联键），靠 `0o600` 权限 + 包注释兜底威胁模型。
- 验证：`go build ./...` ✅ / `go vet` ✅ / `go test ./internal/core/audit ./internal/core/security ./internal/core/transform ./internal/server` 全绿。

**未完成 / 跳过**（fail-loud 声明）：
- 未跑 `http.go` / `context.go` / `scanner.go` 既有测试（审查非 verify）。
- 未读取 `logs/audit.jsonl` 实际内容（可能含真实密钥指纹，按隔离原则不窥探；格式已由单元测试覆盖）。

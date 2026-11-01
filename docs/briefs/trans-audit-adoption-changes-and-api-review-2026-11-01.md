# ParseDock 接入 trans-audit-app：改造清单与 API 评审

日期：2026-11-01　基线：ParseDock `v0.1.1`（`05e39c8`）

## 1. 背景与范围

trans-audit-app 计划把 harness-runtime 以外的「文档 → 文本」解析迁移到 ParseDock，调用方为
backend-python（`parsedock` Python SDK）。涉及的现有路径：

| 路径 | 现状 | 对 ParseDock 的要求 |
| --- | --- | --- |
| 问答解析（`audit_risk_kg/.../doc_parser.py`） | 逐页混合：文本层直读 + 扫描页 docling/RapidOCR；doc/wps 经 LibreOffice 转 docx | Markdown；中文扫描件 OCR；批量吞吐 |
| 治理流水线 / 资料问答（`document_loader.py`） | docling → Markdown | Markdown |
| 法规库入库（`audit_policy_kb/ingest`） | Office 走 LibreOffice UNO 取 `ListLabelString`（保留「第X条」），失败再 docling | **自动编号必须保留**；wps/rtf/odt |

不迁移：财务/纪要的结构化读取（openpyxl 公式与原值、python-docx 段落、python-pptx 纪要），
它们依赖单元格公式与原始值，不属于本次范围；ParseDock 不需要为此提供公式等能力。

调用方是同一内网的后端服务（单 owner 或少量 owner），批量上传（一次 100–600 份，个别 PDF 500+ 页），
部署在离线内网，并会以交付包形式部署到客户环境。

## 2. 实测证据

本地 standalone（`native,xberg,docling-rs`），样本由 python-docx / python-pptx / openpyxl 生成：

| 样本 | `fast` / `auto`（xberg） | `accurate`（docling-rs） |
| --- | --- | --- |
| docx，`List Number` 十进制编号 | `1. 总则…` ✅ | `1. 总则…` ✅ |
| docx，编号 `numFmt=chineseCounting`、`lvlText=第%1条` | `- 总则…` ❌ 编号丢失且被渲染为无序列表 | `- 第一条 总则…` ✅ |
| pptx，带备注页 | 只有正文 ❌ 备注丢失 | 正文 + `备注：…` ✅ |
| xlsx，合并单元格 A5:C5 | 只放在首格，无跨列信息 | `colspan=3` ✅ |
| xlsx，单元格 `data_type` | — | `string` / `number` ✅ |

结论：对 Office 文件，`auto` 当前给出的结果在**编号、备注、合并单元格**上弱于 `accurate`，而
`docs/ENGINE-BENCHMARK.md` 又记录了 docling-rs 在 DOCX/XLSX 纯文本 CER 上更差（0.284 vs 0.082 /
0.254 vs 0.025）。两个引擎各有缺口，不能简单地全部切换引擎。

## 3. 改造清单

ParseDock 是通用的文档解析服务，trans-audit 只是其中一个调用方。所以本节的分级标准是：**不针对
trans-audit 时，这件事是否仍值得做**；能用现有机制解决的，不新增接口；与已有架构决定（ADR、
`docs/ARCHITECTURE.md` §7 Non-goals）冲突的，不在这里直接列为待办。trans-audit 的「接入前必须完成」
不等于 ParseDock 的产品 P0。

分级：**现在做**（通用、成本低）；**接着做**（通用、需要实际开发）；**可选**（有通用价值，等第二个调用方
提出需求或有明确证据再做）；**不做 / 转 ADR**。

### 3.1 现在做

#### 幂等键请求指纹（原 4.2-2）

- 问题：同一 owner、同一 `Idempotency-Key` 上传另一份文件，会静默返回第一个任务。
  `internal/coordinator/service.go:168`（预检查）和 `:285`（插入时唯一约束冲突）两条回放路径都不比较请求内容。
  调用方一旦拼错 key，会把 A 文件的结果当成 B 文件的。这是正确性问题，与调用方无关。
- 做法：`jobs` 表已有 `input_sha256`（`migrations/0001_init.sql:11`），上传时 staged writer 已计算 sha256
  （`internal/storage/store.go:212`），**不需要 migration**。在两条回放路径上比较指纹，不一致时返回 `422`。
  - 指纹只包含影响结果的字段：`input_sha256`、`mode`、`device`。`ttl_seconds`、`priority` 不参与。
  - 需要补一个读取已存哈希的方法，并覆盖并发回放路径（`:285`）。
  - 新错误码按 AGENTS.md 的流程加（`job.go`、`codes.go`、OpenAPI 枚举、`ERROR-CODES.md`、skills、SDK、web types）。
- 行为变化：原来返回 `200` 的请求会改为 `422`，需写进 CHANGELOG。

#### 补全 OpenAPI，与实现对齐

实现已经支持、但 `api/openapi.yaml` 没有声明的部分：

- `/v1/parse` 与 `/v1/jobs` 共用 `stageUpload`，实现已接受 `Idempotency-Key`、`ttl_seconds`、`priority`；
  spec 里只有 `mode`、`device`、`timeout_ms`。
- `POST /v1/jobs/{id}/ack` 已接受 `If-Match` 作为 `result_version`（`internal/coordinator/http.go:281`），spec 未声明。
  只在 `ackJob` 的描述里说明，不加成 header 参数：加参数会改变生成 Java SDK 的 `ackJob` 方法签名，打破现有调用。
- `info.description` 的格式列表缺少 doc/xls/ppt/html（以及 dot/xlt/pot/xlsm），应与 `internal/storage/sniff.go` 的白名单同步。

改完需跑 `make sdk`、`python3 scripts/sync_skills.py`、`npm run api:types`。

#### 离线与中文部署配置

- 在部署文档里给一份推荐配置：`--xberg-ocr-backend=paddle-ocr`（已存在，`cmd/docparse/main.go:202`）、
  模型预置路径、`PARSEDOCK_MAX_UPLOAD_MB` / `PARSEDOCK_MAX_PAGES` 的建议值。
- `--check` 与 worker 启动：启用 paddle-ocr 时，以 `HF_HUB_OFFLINE=1` 对内置图片跑一次真实 OCR，
  模型缺失则报出缺的文件；不按文件路径猜测（xberg 模型清单有 165 个文件，paddle-ocr 只用其中一部分），
  也不把「网络可达」当作就绪条件。worker 没有 `readyz`，所以改为启动即失败。

### 3.2 接着做

#### xberg 丢失 Word 自定义编号与 PPT 备注（原 P0-1）

- 问题：`auto`/`fast` 下 docx 的 `lvlText`（如「第%1条」「（一）」「1.1」）被丢弃，有序列表被渲染成 `-`；
  pptx 备注页被丢弃（见第 2 节）。中文公文、法律文本普遍依赖自动编号，这是通用的保真度缺陷。
- 做法：
  - 先用合成样本复现并固化为 fixture（`decimal`、`chineseCounting`、`ideographTraditional`、多级 `%1.%2`、
    `（%1）`、`w:start`/`lvlOverride` 重新起号、pptx 备注）。
  - 在 xberg 适配层（`internal/engines/xberg.go` / `ooxml.go`）读取 `numbering.xml` 补齐编号，写入已有的
    IR 字段 `metadata.list_marker`；pptx 补读 `notesSlide`。`ooxml.go` 已有为 XLSX 公式打补丁的先例。
  - 同时向 xberg 上游报告。
  - 不要求 `fast` 与 `accurate` 输出逐字一致，只要求编号文本正确。
- **不建议**把 `auto` 下的 docx/pptx 改由 docling-rs 处理：按 `docs/ENGINE-BENCHMARK.md`，DOCX 纯文本 CER
  会从 0.082 劣化到 0.284。最多作为临时过渡，并在文档中写明代价。
- 估时：完整处理 abstractNum、lvlOverride、重新起号和样式关联，3–4 天偏乐观，按 4–6 天预留。

### 3.3 可选

| 项 | 通用价值 | 为什么不现在做 | 调用方的现成替代 |
| --- | --- | --- | --- |
| LibreOffice 转换（wps/et/dps/rtf/odt，原 P0-2） | 中文市场有通用需求 | 引入数百 MB 依赖、中文字体和新的子进程沙箱面。要做也应是可选 flag + 单独的 worker 镜像变体，不进默认路径 | 调用方继续先转成 docx/xlsx/pptx 再上传 |
| 拆分 `409 conflict`（原 4.2-1） | 客户端不必解析 message | 每个新码都要走完整的错误码流程。只拆客户端真正需要区分的两类：`result_not_ready`（稍后重试）和版本不匹配（重新获取 `result_version`）。新增枚举需附迁移说明：严格枚举反序列化的第三方客户端可能不兼容 | 先查 `GET /v1/jobs/{id}` 的状态再下载或 ACK |
| 批量状态查询 `GET /v1/jobs?ids=`（原 P1-2、4.2-3） | 所有批量调用方受益 | 当前只有一个调用方提出；不做 label 检索和分页 | 限制调用方并发；SDK 已按 `Retry-After` 退避（`sdk/python/parsedock_sdk/client.py:137`） |
| `RateLimit-*` 响应头（原 P1-2、4.2-8） | 便于客户端控速 | 限流是**每 owner、每 API 副本**一个令牌桶（`cmd/docparse/main.go:188`，默认 20），多副本下 `Remaining`/`Reset` 的语义说不准，需要先定义 | 已有 `Retry-After`；运维可调 `--rate-limit` / `--rate-burst`（`docs/RUNBOOK.md:219`） |
| 任务 `labels`（原 P1-3） | 便于排查 | 暂无第二个调用方的需求。若要做，只在 Job 资源上回显，**不写入**审计日志和事件（审计日志只允许 id、大小、引擎等字段，见 `docs/ARCHITECTURE.md` 审计日志一节） | 调用方自己保存 job_id ↔ 业务 id 映射；崩溃恢复用幂等键 |

### 3.4 不做 / 转 ADR

- **`source_url` 输入与 Webhook 回调（原 P2-1、P2-2）**：`docs/ARCHITECTURE.md` §7 与 ADR-014 规定，
  这两项需要单独的设计评审（SSRF、DNS rebinding、重定向、HMAC、重放保护）。它们没有被永久禁止，
  但不能在接入简报里直接列为待办。若推进，先写 ADR；拉取只用普通 HTTP GET，不引入 MinIO/S3 SDK
  （AGENTS.md 硬规则 2）。另外，输入字节仍经过 coordinator（`docs/ARCHITECTURE.md` §8 指出它是网络瓶颈），
  省掉的只是调用方的一跳。

### 3.5 不属于本次接入：许可证 L1

worker 镜像带有 `libx265`（GPL-2.0），`docs/LICENSE-REVIEW.md` 标为 BLOCK。这是 AGENTS.md 中已记录的
「Deliberate gaps」，属于产品分发决策，与 trans-audit 接入无关，因此不列入本次改造。但只要以交付包形式部署到
客户环境，就**必须先解决**，不能因为它是已知缺口而绕过。

## 4. API 最佳实践评审

对照对象：RFC 9110（HTTP 语义）、RFC 9457（Problem Details）、IETF Idempotency-Key 与 RateLimit
头草案、Google AIP（长时操作 / 自定义方法）、Stripe 等公开 API 的通行做法。

### 4.1 做得好的地方（保留）

- 异步任务模型清晰：`202 + Location`，处理状态与保留状态是两个独立维度，`GET /v1/jobs/{id}` 是唯一权威。
- ACK 与 cancel 职责分离，下载租约保证「ACK 不会截断正在进行的下载」，tombstone 期间返回 `410` 再转 `404`，语义严谨。
- 每种格式各自的 `ETag` + `If-None-Match → 304`，`X-Result-Version` 作为全局版本，符合 RFC 9110 的条件请求。
- 别人的任务与不存在的任务一样返回 `404`，避免泄露任务是否存在；scope 粒度合理（`jobs:write/read/ack`）。
- 错误码稳定、机器可读、带 retry 语义说明；`429` 区分 `rate_limited` 与 `quota_exceeded`，并带 `Retry-After`。
- 路径带版本 `/v1`，字段统一 snake_case，时间统一 RFC 3339，ID 有前缀（`job_` + ULID），IR 带 `schema_version` 常量。
- SSE 可用 `Last-Event-ID` 续传，游标过期时发 `cursor_reset`，不会静默丢事件。
- 生成 SDK 时开启了 `enumUnknownDefaultCase=true`，新增枚举值不会让用这两个生成 SDK 的客户端崩溃。

### 4.2 逐条结论

| # | 问题 | 结论 | 去向 |
| --- | --- | --- | --- |
| 1 | `409 conflict` 承载多种语义（`service.go` 1259/1263/1280/1318/1382 与 `http.go:646` 共 6 处） | 有价值，但只拆两类：结果未就绪 / 版本不匹配 | 3.3 可选 |
| 2 | 幂等键复用、请求体不同时静默回放 | 正确性修复，成本低（已有 `input_sha256`） | 3.1 现在做 |
| 3 | 没有列表 / 批量查询接口 | 批量按 id 查询可选；label 检索、游标分页、固定 24h 保留属过度设计 | 3.3 可选（仅 `ids=`） |
| 4 | `POST /v1/jobs` 参数在 query、表单、header 三处重复，生成的 Java 方法有 10 个参数 | 问题真实。但直接从 spec 删除 query 版本会改变生成 SDK 的方法签名，属破坏性变更。先改善手写 SDK 包装层；删除放到下一个允许破坏性变更的版本，并附迁移说明。服务端继续接受 | 延后 |
| 5 | ~~`/v1/parse` 不支持幂等键、TTL、priority~~ | **原陈述有误**：实现已支持，只是 spec 未声明。补 spec；`/v1/parse` 只返回 JSON IR 的限制暂不扩展（同步接口定位是小文档、交互式调用） | 3.1 补 spec |
| 6 | 错误体不是 RFC 9457 | 不换 media type；可在错误体中加 `request_id`（目前只在 header）。`details` 按需再加 | 可选，小改动 |
| 7 | ~~ACK 的版本校验没有用 HTTP 条件请求~~ | **原陈述有误**：ACK 已接受 `If-Match`（`http.go:281`），只是 spec 未声明。补 spec；不顺手改成 `412`，那是契约变化，需单独评估 | 3.1 补 spec |
| 8 | 缺少限流响应头 | 多副本语义需先定义；先用 `Retry-After` | 3.3 可选 |
| 9 | 0.1 就带了废弃别名（`processing_status`、`max_lifetime_at`） | 没有证据表明它们带来维护成本，不删 | 不做 |
| 10 | `format` 同时接受 `md` 和 `markdown` | 同上，删除收益不足 | 不做 |
| 11 | `info.description` 格式列表与实现不一致 | 应修，纯文档 | 3.1 补 spec |
| 12 | 没有能力发现接口 | `--check` 是运维工具，不能替代远程发现，但目前只有一个调用方；先完善部署文档，不对外暴露内部路由表 | 暂缓 |

### 4.3 不建议修改（说明理由）

- `POST /v1/jobs/{id}/ack`、`/cancel` 这类动作式子资源：长时操作的取消、确认用自定义方法是通行做法（AIP-136），不必改成 `DELETE` 或 `PATCH`。
- ACK 返回 `202` 而不是 `204`：删除是异步的，`202` 更准确。
- 不对外暴露 coordinator 和内部接口：保持现状。

## 5. 版本建议

版本号按实际的契约影响来定，不按事先分好的组打包。

- **补丁版本（0.1.x）**：只放不改变契约的修正，例如部署文档。注意：给 `/v1/parse` 补 spec 参数会让
  生成 SDK 的 `parseSync` 多出参数，Java 调用方需要改代码，所以 3.1 的 spec 补全不是纯补丁级改动。
- **下一个小版本（0.2.0）**：
  - 幂等键指纹（行为变化：`200` 改为 `422`，CHANGELOG 写明）；
  - 编号 / 备注修复（3.2）；
  - 如果做了 3.3 中的项，也放在这里。新增错误码需附迁移说明。
  - Go 二进制与两个 SDK 同步发版（`scripts/check_versions.py`）。
- **破坏性清理**（原 4.2-4 删除 query 参数）：先在一个版本中标记废弃并写明迁移方式，再在之后的小版本删除。
- **许可证 L1**：与版本号无关，首次以交付包形式部署到客户环境之前必须解决（见 3.5）。

3.1 的三项已在同一分支实现（见 `docs/CHANGELOG.md` Unreleased）。第 3–5 节按通用服务的定位修订过（2026-11-01），修订依据为对照代码的复核，以及另一位评审者（Codex）的独立评审与讨论。

## 6. 附录：复现第 2 节

```bash
export PARSEDOCK_INTERNAL_TOKEN=$(openssl rand -hex 24) KEY=$(openssl rand -hex 24)
export PARSEDOCK_API_KEYS="$KEY:t"
./runtime/bin/docparse --role standalone --engines native,xberg,docling-rs \
  --xberg-bin ./runtime/engines/xberg-cli/xberg --xberg-lib-dir ./runtime/engines/xberg-cli \
  --docling-rs-bin ./runtime/engines/docling-rs/docling-rs \
  --docling-rs-models ./runtime/engines/docling-rs \
  --listen 127.0.0.1:18990 --internal-listen 127.0.0.1:18991 \
  --data-dir /tmp/pd/data --work-dir /tmp/pd/work &
# 生成「第%1条」样本：python-docx 写入 List Number 段落后，把 numbering.xml 中
# ilvl=0 的 numFmt 改为 chineseCounting、lvlText 改为「第%1条」
for m in fast accurate; do
  curl -s -X POST localhost:18990/v1/jobs -H "Authorization: Bearer $KEY" \
       -F file=@num2.docx -F mode=$m
done
```

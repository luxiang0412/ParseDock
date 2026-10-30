# ParseDock — Phase 1 Readiness / PR-00 审查与修订任务书

> 审查基准：2026-10-30。基于用户提供的 9 份文档的交叉审查；**未审计完整源码、未复跑 Phase 0 测试**。文档中的 PASS 是现有项目的报告结果，不代表本次独立验收。
>
> 本文目标：在保留 Go + SQLite + Coordinator + 无状态 API + 本地临时目录 + CPU/GPU Worker 的前提下，收敛 Phase 1 前的协议和决策矛盾。

## 0. 总结与执行边界

- 允许开始 Phase 1 工程工作；PR-01（CI）可与 PR-00 并行启动。
- **PR-00 必须在公共 API/IR 冻结、Java/Python SDK 发布、生产镜像外部分发前完成。**
- Xberg `libx265` 许可问题是外部分发阻断项，不应因本次审查被降低级别；应由合法合规责任人明确选择移除、替代或合规分发，并提供证据。
- 当前已验证的是同机多进程/容器和本机 GPU；跨主机网络故障与 GPU Worker 镜像尚未完整实测，继续由 PR-07/08 验收。
- 不引入 Redis、PostgreSQL、对象存储、外部队列、强制模型云服务。

## 1. 问题清单与修订要求

### F01 [P0] 引擎质量评分的公式与文档不同步

证据：`docs/ENGINE-DECISION.md` §2 与 `docs/ENGINE-BENCHMARK.md` §7。

- 在现有 Benchmark 子维度分数和权重 `0.40,0.25,0.15,0.10,0.10` 下，**docling-rs = 7.21**（报告 7.4）、**Tika = 6.31**（报告 6.5）、**MinerU flash = 5.04**（报告 5.1）、**xberg = 8.145**（报告 8.1）。
- `ENGINE-DECISION.md` 对 docling-rs 的 CER 计算列出 7 个值，但分母写为 7，未正确计入两份扫描件的双权重，且列出的 CJK image 项没有被完整纳入加总。以列出的七个值和扫描 EN/ZH 双权重为准，权重总和应为 9，加权 CER ≈ 0.295922，基于 `8*(1-CER)+2*table_accuracy` 得分 ≈ 7.6326；这**不是**质量结论的最终定稿，因为两个文档采用的样本集和规则尚未统一。
- 评分按引擎可覆盖的不同样本计算，会产生不可比性；部分 CER > 1，公式还需明确截断及缺测处理。

**修订**：读取原始 bench JSONL/CSV，先固定同一“适用格式集合”与缺测规则，再用可测试的 Python/Go 脚本自动生成 `ENGINE-BENCHMARK.md` 和 `ENGINE-DECISION.md` 的评分段落。保留引擎分工思路，不根据错误分数仓促换引擎。

**验收**：权重和为 1；输入样本集显式记录；任何样本/权重变化可复算；两份文档数字一致；单元测试覆盖遗漏项、双权重、CER>1、缺测。

### F02 [P0] `accurate` 名称与实际路由的质量承诺不一致

证据：`docs/ENGINE-BENCHMARK.md` §2、`docs/ENGINE-DECISION.md` §3、`docs/DOCUMENT-IR.md` §5。

- docling-rs 对英文/中文原生 PDF 文本更准确，但英文扫描件 CER 0.884（xberg 0.055）；对 DOCX/XLSX 的 CER 也高于 xberg（DOCX 0.284 vs 0.082；XLSX 0.254 vs 0.025）。
- docling-rs 保留 XLSX sheet 名和合并单元格结构，xberg 缺 sheet 名；所以**文本 CER 与结构完整度不能混为一谈**。
- `ENGINE-DECISION.md` 的 CSV `accurate → xberg` 路由，与 `DOCUMENT-IR.md` 所称 GB18030 CSV 不走 xberg 的描述存在冲突。

**修订**：把质量维度定义成 text fidelity / structure fidelity / provenance fidelity；`fast`、`accurate`、`auto` 是策略偏好，不承诺某引擎在所有文档都更准。TXT/MD/CSV/TSV 统一优先 native（任何 mode），并用 GB18030 作为回归场景。原生 PDF、扫描 PDF、Office 路由先明确可测规则；无法可靠判断的场景输出 warning 和 `routing.reason`。不得仅依据 24 份合成文件直接建立语言特化的硬编码生产路由。

**验收**：各 format×mode×device 有表驱动路由测试；CSV 所有 mode 正确解码；准确模式出现已知降级时有显式记录；不发生隐式 CPU/GPU 降级。

### F03 [P0] ACK、Cancel、Sync、TTL 和终态语义需一致

证据：`api/openapi.yaml` `/v1/jobs/{id}/ack`、`/cancel`、`/v1/parse`；`docs/JOB-LIFECYCLE.md` §1–§4。

- `POST /cancel` 的描述允许释放 completed 结果，但 `POST /ack` 首次释放需要 `result_version`。不能让普通取消绕过这个约束。
- `/v1/parse` 200 响应完成后即删临时结果，其语义不同于异步“客户端明确 ACK”；可以保留，但必须明确其为 **one-shot、不保证断线后恢复** 的模式；超时返回 202 后转入完整异步 ACK/TTL 流程。
- `max_lifetime_at = created + 24h` 目前在清理规则中主要约束 queued/running，已完成结果还有独立 commit+TTL；名称“绝对最大生命周期”容易误解。建议分离 `processing_deadline_at` 与 `result_expires_at`，或明确它对结果不构成全局删除上限。
- PR-03 描述 worker 收到 heartbeat 409 cancelled 后再 POST `/fail`，要核对此时 token 是否已失效；需设计可达的取消完成转换，而不是要求调用一个会被拒绝的结束接口。

**修订**：定义状态转换表与一组明确不变量：completed 结果仅经版本匹配 ACK 或 TTL 释放；未完成任务取消走 cancel-requested→worker-stop→cancelled/timeout；下载租约与删除状态在原子条件下互斥；同步 one-shot 和异步转换均有规范。

**验收**：cancel completed 不绕过 ACK；不同阶段取消；heartbeat/worker finalization 不自相矛盾；同步 200 和超时 202、断连行为测试；清理并发压力测试。

### F04 [P0] 鉴权主体与 API 代理边界

证据：`api/openapi.yaml` info 描述“job owner derived from bearer token”；`docs/CLUSTER-LITE.md` §2、§6；`docs/IMPLEMENTATION-PLAN.md` PR-02、PR-07。

- Key 轮换不应造成已创建任务的所有者身份变化。`owner_id` 必须与 key secret/hash 解耦：多个活跃/轮换 key 映射到同一稳定 owner。
- API 需要剥离客户端伪造的 `X-Parsedock-Owner`、内部 token、租约头；Coordinator 只接受来自可信内部连接的身份上下文。静态共享 token + 私网并不等同于强认证；mTLS 在 PR-07 交付前，必须确保 internal endpoint 不公开。

**验收**：轮换前后的 key 能访问同一 owner 的 job；跨 owner 404；恶意外部注入内部头不能越权；worker token 无法直接伪造 owner。

### F05 [P1] HTTP 表示 ETag 与 ACK 版本不同职责

证据：`docs/JOB-LIFECYCLE.md` §7 声称 manifest version 是每种结果的 ETag；`api/openapi.yaml` download headers。

- `X-Result-Version` = manifest SHA-256，保持供 ACK 使用。
- Markdown、Text、JSON 的 **强** ETag 应匹配各自实际表示的 bytes；启用 gzip/br 后还需与 `Content-Encoding`/`Vary` 一致。不能三种不同字节表示都共用同一个强 ETag。
- ACK 版本只能证明调用方拿到了版本标识，并不提供“已实际消费 bytes”的密码学证明；客户端确认是业务语义。

**验收**：同一 job 三种 format 的 manifest version 相同，强 ETag 对不同表示正确区分；304/conditional GET（若支持）与压缩表示测试；SDK ACK 使用 `X-Result-Version` 而非 ETag。

标准参考：RFC 9110 §8.8.3 https://www.rfc-editor.org/rfc/rfc9110.html

### F06 [P1] OpenAPI 3.1 schema 与 SDK 契约

证据：`api/openapi.yaml` 使用 `openapi: 3.1.0`，有 8 处 `nullable: true`；同一规范包含 `/internal/*`。

- 3.1 JSON Schema 中应使用 `type: [string, 'null']` / `anyOf` 等形式，不使用 3.0 风格 nullable。
- 建议拆成 `api/openapi.yaml`（公开 API）和 `api/openapi.internal.yaml`（Worker/Coordinator 内部 API）；只从公开规范生成 Java/Python SDK。拆分后需确保版本、类型、错误码和权限一致。
- `POST /v1/jobs` 要求 multipart 标量字段出现在 `file` 之前、query 又覆盖 multipart，可能导致生成 SDK 与通用客户端不兼容。优先允许**任意 multipart part 顺序**（可以落盘暂存并做容量限制），或仅用 query 指定策略字段，去掉有序 part 要求。确保 streaming 不把整个文件放进内存。

**验收**：OpenAPI 3.1 lint+schema validation；Java/Python 生成客户端可上传、下载、ACK；内部接口不进入公开 SDK；part 任意顺序行为固定并测试。

标准参考：https://learn.openapis.org/upgrading/v3.0-to-v3.1.html

### F07 [P1] SSE 生命周期与内部协议缺口

证据：`api/openapi.yaml` `/events` 的事件定义包含 `released`/`expired`，又宣称 job terminal 时 stream_end；`docs/CLUSTER-LITE.md` 的 worker heartbeat 接口在 OpenAPI 中缺失。

- 必须选择 SSE 订阅边界：只到处理 completed/failed/cancelled，还是继续覆盖 retention 释放阶段；一旦明确，写入 SDK 契约与断线重连规则。
- `Last-Event-ID` 重放窗口与 10 分钟 tombstone / 事件删除期限要一致；游标过期时发送 snapshot 或明确定义错误码。
- 将 `/internal/workers/{id}/heartbeat`、已有 diagnostics 等真实内部接口放入 internal API spec，避免文档漂移。

**验收**：SSE 断线恢复、有序事件、终止条件一致、游标过旧处理；internal spec/实际 router 一致。

### F08 [P1] Document IR 的真实能力边界与数值准确性

证据：`docs/DOCUMENT-IR.md` §2–§6。

- `image_ref` 已定义但当前没有 image artifact 发布与下载方式；第一版应明确“仅 schema 预留，当前不可用”，不能输出一个不可访问的引用。如果后续激活，新增经过授权的 artifact GET 接口并纳入 ACK/TTL。
- XLSX 公式文本当前没有被两套引擎提供，而 PR-05 计划填补；可评估从 OOXML 工作表 XML 的 `<f>` 读取公式，并与 sheet/cell/cached-value 精确映射。须涵盖 shared formulas、日期、空值、数字精度；不得编造。
- bbox 坐标明确了坐标空间，但还应有规范的 page dimension / rotation（如涉及网页高亮）以便转换。缺少时不构造像素框。
- 强化 JSON Schema 引用、阅读顺序、跨表关联及 warning 代码回归。

**验收**：所有已发布的 image_ref 可访问或不产生；两工作表+合并单元格+公式+缓存值 golden tests；定位信息不丢失，所有缺失信息明确 warning。

### F09 [P1] Worker Scratch 回收、取消与数据保留

证据：`docs/CLUSTER-LITE.md` §6 仅提 worker 重启清理 `runtime/work`；`docs/JOB-LIFECYCLE.md` §6–§7 对 Coordinator 侧清理完备。

- 远程 Worker 的临时输入和中间文件可能含敏感文档；不应只在进程启动时清理。需要每个 attempt 的 `defer` 清理、运行中 periodic orphan sweep、crash-restart 恢复、最大空间/存活期，防止断连与长运行时不断堆积。
- 清理策略要避免清理仍处于合法运行租约的文件；与 Coordinator 的版本、租约和 lease-fencing 一致。

**验收**：engine SIGKILL、worker SIGKILL、网络断连后本地 scratch 最终回收；运行中清理不误删正在工作的 attempt。

### F10 [P1] 可扩容≠高可用；SLO 和备份不能与 ACK 语义冲突

证据：`docs/CLUSTER-LITE.md` §1、§7；`docs/IMPLEMENTATION-PLAN.md` PR-09、PR-12。

- Cluster-lite 目前只是同机多进程验证，不是实测跨机；PR-07 必须做真实双机、网络分区、持续 load/soak。
- PR-09 的“result-loss budget zero for **acknowledged** results”语义有误：ACK 后结果应该被删除。应把目标写为“未 ACK、未过期且已提交的结果在受支持故障模型内不意外消失”；明确 Coordinator 磁盘丢失属于当前单点，不能保证零损失。
- PR-12 备份原始文件/结果可能让已 ACK 删除的内容从历史备份复活。默认备份配置、密钥与非敏感状态；如要备份有效结果，明确加密、保留期、恢复时 TTL/ACK 再校验及删除传播，不得承诺永久保留。

**验收**：文档 SLO/备份/恢复与隐私留存约束一致；恢复测试中已释放/过期结果不会重新可下载。

### F11 [发布阻断] 许可证与依赖闭包

证据：`docs/LICENSE-REVIEW.md` §2、§7。

- 首选构建移除 `libx265` 的 Xberg 变体，验证它仍能处理目标格式，形成 SBOM/链接依赖闭包记录；备选路径需要法律审核并确认源码提供、替换性和通知义务。仅“作为子进程”不自动解决 GPL 分发问题。
- MinerU 只有获批准的本地 flash 档位，文档规定阻断远端上传和 telemetry；GPU image 在 PR-08 中必须真正测试网络出口与许可证声明。
- GPU worker 镜像对外分发前审查 CUDA 运行时再分发条款。第三方模型和依赖锁定版本与 hash。

**验收**：发行清单、许可证/NOTICE/SBOM、镜像依赖审计报告；无法批准的组件默认不随外部分发镜像提供。

## 2. PR-00 切片与依赖建议

| PR | 任务 | 发布门槛 |
|---|---|---|
| PR-00A | Score source-of-truth、同语料评分、路由测试 | 所有模型决策记录一致 |
| PR-00B | ACK/Cancel/Sync/SSE/TTL 状态机与 HTTP 契约 | 对外 API 冻结前完成 |
| PR-00C | OpenAPI 公私分离、3.1 schema、SDK smoke、身份与 ETag | SDK 发布前完成 |
| PR-00D | IR 能力边界、worker scratch、license gate、SLO 修订 | 生产镜像发布前完成 |

尽量以一个 PR-00 主分支、按测试/文档分提交；若 CI 规则要求，也可独立为 4 个可合并 PR。PR-01 CI 可并行，但不要把不一致契约固化成 generator golden。

## 3. Pi 直接执行指令

> 你是 ParseDock 的架构和工程审查负责人。请完整阅读本报告及其引用的 9 份源文档，并在仓库中核对实际实现。先不要重写核心架构，也不要连续实施 PR-01…PR-12。
>
> 1. 建立 `docs/PR00-ISSUE-MATRIX.md`，每一条 F01–F11 标注“证据位置 / 当前代码行为 / 建议修订 / 对外兼容性影响 / 测试 / 是否阻断发布”，纠正本报告中任何经代码证明不准确的推断。
> 2. 先复核引擎评分，使用 raw benchmark 记录编写自动评分脚本，重新生成两份评分表。不得伪造数据；语料不足时标记低置信度。
> 3. 审查 `api/openapi.yaml` 与 Router/SDK 的逐项一致性，提出最小兼容改法，尤其处理 Cancel、Sync、ACK、SSE、ETag、3.1 nullable、multipart 字段顺序、公私规范分离。
> 4. 审查 Document IR 和所有 adapter：结构、公式、sheet、bbox、image_ref、warning；禁止填充不存在的来源数据。
> 5. 通过源码验证 SQLite+FS 原子清理、下载租约与新的下载并发、Worker scratch 回收；检查稳定 owner ID 与 key 轮换边界。
> 6. 检查 Worker/Coordinator 协议、X-Parsedock-Owner 身份边界、GPU 路由和外部访问控制，保留 cluster-lite 的单点声明。
> 7. 检查许可证真实打包物，包括 xberg `libx265`、MinerU 许可、CUDA 镜像。许可证责任的最终结论留给法律审核，不得以“子进程隔离”直接宣称合规。
> 8. 为已确认的 P0/P1 问题实施最小可审查修订，更新测试、OpenAPI/SDK、相关文档。涉及语义突破性变化先记 ADR，并在报告中写明兼容迁移方案。
> 9. 执行 `make test`、`make acceptance`、`make acceptance-real`、`make sdk-test` 以及可运行的契约与镜像测试。需要外部资源或双机场景的验证按 PARTIAL 标明，不能伪称 PASS。
> 10. 交付 `docs/PHASE1-READINESS.md`、`docs/PR00-ISSUE-MATRIX.md`、各项测试结果、变更清单及更新后的 `docs/IMPLEMENTATION-PLAN.md`，并给出 GO / CONDITIONAL GO / NO-GO 的判断。
>
> 边界：保留 Go 单二进制四角色、SQLite WAL、本地目录、ACK+TTL、无状态 API、CPU/GPU Worker、Docker；不新增强制外部依赖，不向公网暴露 internal API，不擅自改动宿主机或其他项目。先解决会造成长期返工的契约矛盾，不做不相关重构。

## 4. 关键验收测试（建议 ID）

- `PR00-01`: 评分脚本的权重、CER、缺测和报告同源测试。
- `PR00-02`: TXT/CSV/GB18030 在 auto/fast/accurate 下均不出现乱码。
- `PR00-03`: 完成 job 的 Cancel 不绕过 result_version ACK；未完成任务按确定状态机取消。
- `PR00-04`: 同步 200 的 one-shot 行为，超时 202 转异步，断连重试行为确定。
- `PR00-05`: ACK 与新下载/已开始下载的竞争保持互斥且无 UAF/孤儿文件。
- `PR00-06`: 同一 job 不同格式/不同 Content-Encoding 的 ETag 语义正确；ACK 用 result_version。
- `PR00-07`: SSE replay、终态、reconnect/tombstone 规则与 API 文档一致。
- `PR00-08`: 两份 OpenAPI 分别校验；Python/Java 公共 SDK 不含 internal operations。
- `PR00-09`: multipart 文件部件位于前/后均按契约处理；上传流式且资源受限。
- `PR00-10`: key rotation 保持 owner 连续性，伪造内部身份头无法越权。
- `PR00-11`: image_ref 不悬空；formula、sheet、cell mapping golden fixtures。
- `PR00-12`: Worker crash/network partition 后 scratch 清理最终完成。
- `PR00-13`: 清理/备份恢复不会让 ACK 或 TTL 过期结果重新可下载。
- `PR00-14`: GPU required 与 Worker 能力匹配，不假定 docling-rs 支持 GPU；未验证的 GPU image 标 PARTIAL。
- `PR00-15`: GPL/NOTICE/SBOM 分发门禁与受控镜像供应链检查。

## 5. 本次审查可确认和不可确认的内容

**可以从文档确认**：方案描述、版本、作者报告的测试覆盖、当前已知缺口、跨文档不一致、分值计算问题。

**不能仅凭文档确认**：当前仓库源码是否与描述一致、实际双机稳定性、完整图像产物下载、真实文档泛化质量、法律上可以分发哪些二进制。它们必须通过代码审计、独立实测或法律审核完成。

## 6. 依据文件

- `docs/PHASE0-SUMMARY.md`
- `docs/ENGINE-DECISION.md`
- `docs/ENGINE-BENCHMARK.md`
- `docs/IMPLEMENTATION-PLAN.md`
- `docs/CLUSTER-LITE.md`
- `docs/DOCUMENT-IR.md`
- `docs/JOB-LIFECYCLE.md`
- `docs/LICENSE-REVIEW.md`
- `api/openapi.yaml`

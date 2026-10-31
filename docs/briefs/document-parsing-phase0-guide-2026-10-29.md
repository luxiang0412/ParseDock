# Document Parsing Service — Phase 0 技术调研与 PoC 任务书

> 版本：v0.1 · 2026-10-29  
> 面向：本地 AI Coding Agent（Pi）与后续实施团队  
> 阶段：**先研究、验证和设计；不是直接建设完整生产系统**  
> 目标：最小外部依赖、可无状态扩容 API、可分布部署 CPU/GPU Worker、可插拔文档解析引擎。

---

## 0. 执行指令（给 Pi）

你是一名负责技术调研与架构原型的工程 Agent。请严格按本任务书推进；先检查环境、风险和许可证，再执行受控 PoC。不得以 README 宣称的性能代替实测，不得直接假设“Go / Rust / GPU 一定更快”。必须保留所有测试的命令、输入、版本、配置和原始结果。

**工作边界**：

1. 优先在新的隔离工作目录、容器、独立端口和专用数据目录中运行测试；不得修改已有应用服务、现有数据库、系统防火墙或共享 Docker Compose 堆栈。
2. 不要求安装 Redis、PostgreSQL、MinIO、Kafka、RabbitMQ、NFS 或云服务。若发现现有机器缺少必要依赖，应记录缺项和替代方案，不得擅自对主机执行高风险变更。
3. 解析引擎的安装、模型权重下载和 GPU 驱动依赖可能复杂。每个候选单独隔离；CPU-only 模式必须完整可用，不能在启动时强制加载 CUDA 依赖。
4. 涉及远程模型下载、第三方服务调用、远程 URL 读取、真实用户文件、破坏性清理与潜在费用的行为，必须明确记录并获得相应授权。默认使用公开或自造的非敏感测试文件，不将测试文件上传第三方云端。
5. PoC 可写必要的测试原型、脚本、容器定义和文档，但**暂不实施复杂 UI、RAG、向量数据库、用户管理平台、完整多活高可用**。
6. 发现方案不可行时给出证据、替代路径和决策建议，不要通过加入新基础设施偷偷绕过约束。

### 完成的定义

Phase 0 结束时，必须可以从文档、代码和原始数据中复现：至少一个 CPU 解析端到端流程、ACK/TTL 的可靠性验证、API 多副本访问同一任务、远端 Worker 文件流与任务回传的最小闭环；GPU 可用时补 GPU 验证，不可用则交付可执行的验证方案与记录的阻塞原因。

---

## 1. 产品目标与固定约束

### 1.1 业务场景

面向 Chat 附件、Agent 工具、RAG/知识库及其他服务，将 PDF、DOC/DOCX、XLS/XLSX、CSV/TSV、TXT/Markdown、PNG/JPG/WebP 等转换成适合模型消费的文本和结构化文档。PPTX 可作为优先扩展项。应保留表格、章节、页码、工作表/单元格来源，必要时支持 OCR。

### 1.2 必须满足（P0）

- **协议**：统一 HTTP API，OpenAPI 契约；同步与异步两种访问方式；Java/Python SDK 可生成并可增强封装。
- **生命周期**：明确 ACK API；收到有权限且版本匹配的 ACK 后尽快释放资源；未 ACK 的产物按 TTL 回收；支持最大任务存活时间和磁盘配额。
- **依赖**：单机仅依赖本地磁盘与可选 SQLite；无强制外部数据库、消息队列或对象存储。
- **多副本**：API 无状态，任意 API 副本可访问某个任务；单机可多 API 实例，跨机通过单一 Coordinator 管理状态和文件。
- **加速**：CPU-only 可工作；GPU Worker 可选；Docker 与 Docker Compose 可部署；Worker 按能力匹配任务。
- **质量与资源**：区分原生文本解析与 OCR/VLM 重型解析；量化延迟、精度、内存、CPU/GPU 使用和并发表现。
- **安全**：文件上传是非可信输入；强制大小、格式、页数、总执行时间和资源边界，避免路径穿越、Zip Bomb、SSRF 和解析器恶意输入。

### 1.3 明确不在第一版范围（P1/P2）

- 完整分布式多活 Coordinator、Raft/共识、跨主机 SQLite WAL。
- 用户账号平台、计费、组织管理、GUI、复杂权限后台。
- 向量化、RAG Chunking、Embedding、LLM 摘要等超出文档解析的能力。
- 任意远程 URL 读取、云端存储连接器。未来添加 URL 功能前需单独评审 SSRF 和出网策略。
- 把所有引擎同时投入生产。实验完成后最多先落地 1 个快速引擎 + 1 个高质量引擎。

---

## 2. 技术选型假设与待验证事项

### 2.1 控制层

**暂定 Go**：实现 HTTP Gateway、Coordinator、SQLite 队列、Worker 客户端、文件流、ACK/TTL、SSE、Webhook。选 Go 的依据是二进制部署、HTTP/并发与工程运维便利；不要将它视为对解析速度具有已证实优势。解析时间主要由内部引擎与模型决定。

建议设计 `cmd/docparse` 单二进制，通过 `--role standalone|api|coordinator|worker` 选择角色；也可按具体可维护性采用多个二进制。先写 ADR，比较单二进制与多二进制的构建/部署取舍。

控制层不直接耦合 Docling/Python 等引擎数据类型；接口通过内部适配器契约沟通。最初可以以本地受控子进程或内部 HTTP 形式接入。

### 2.2 候选解析器（以版本锁定和实测为准）

| 候选 | 原因 | 首要实验 | 注意点 |
|---|---|---|---|
| Xberg（Kreuzberg 后继） | Rust 核心，广泛格式，HTTP/CLI/Docker | 原生 PDF、Office、CSV 延迟与内存 | 项目宣传的格式数量/性能需要逐类验证 |
| docling.rs | Rust Docling 兼容路线，HTTP/CPU/CUDA 镜像 | 与 Python Docling 对照、PDF/Office 精度和 CPU 速度 | PDF ML 一致性尚非完全相同，CPU/GPU 模型和镜像须明确 |
| Docling + docling-serve | 结构化 `DoclingDocument`、布局/表格 | 标准 JSON、版面与中文表格质量 | Python/模型启动开销、GPU 依赖，内置任务能力不直接当公共契约 |
| MinerU 4.1 | Flash/Basic/Standard/Advanced 分层 | 扫描件、公式、复杂表格、GPU | 版本变化、模型/许可条款、资源开销 |
| Apache Tika | 广泛格式、Java 生态、成熟兜底 | 纯文本提取基线与边缘格式 | 不是所有文件都能保留高质量页面布局 |

**候选退出规则**：无法稳定安装、许可证不适合服务部署、目标语言/格式明显缺失、启动成本过大、质量低于基础要求，须记录证据后方可退出，不可静默跳过。

**许可证 P0 检查**：逐项审查主代码 LICENSE、模型权重 license、第三方依赖与容器镜像的商用限制、网络服务署名要求；写入 `docs/LICENSE-REVIEW.md`。特别对 MinerU 当前仓库条款进行精确核验；不要只根据仓库的 SPDX 标签推断商用许可。

### 2.3 初步引擎路由，不得在未实测前写死

- `TXT/CSV/TSV/Markdown`：原生或流式解析优先；避免加载 OCR 模型。
- `DOCX/XLSX/PPTX`：原生结构提取优先；图中文字另行说明为可选能力。
- 文本层可靠的 PDF：先尝试快速路径，检测版式风险后选择是否升级。
- 扫描 PDF / 图片：OCR；图表/公式/多栏重度排版可选择结构化或 VLM 路径。
- 选择策略：`mode=auto|fast|accurate`，设备策略 `device=auto|cpu|gpu_preferred|gpu_required`。路由决策与实际引擎版本写入结果元数据。
- GPU 不可用时 `gpu_required` 不允许静默降级；`gpu_preferred` 允许显式标注的 CPU fallback。

---

## 3. 部署拓扑与无状态边界

### 3.1 Standalone：最小部署

单主机内：API + Coordinator/任务调度 + SQLite WAL + 专用文件目录 + CPU Worker（可选 GPU Worker）。可单容器起步，也可拆成多个 Compose 服务。多个 API 进程必须读写同一个 Coordinator 或在同机正确协调 SQLite。**不得将 SQLite WAL 放到跨机器共享文件系统使用。**

### 3.2 Cluster-lite：跨机器 CPU/GPU Worker

- 任意数量无状态 API 实例只接受请求、做鉴权/限流与流式转发，不保存持久文件与任务状态。
- **一个** Coordinator 独占 SQLite、输入文件、结果文件；提供内部任务/文件 API。
- 远程 Worker 通过内部网络领取任务、流式读取源文件、运行解析引擎、将结果回传 Coordinator，并上报心跳与真实进度。
- 任务领取使用租约 + fencing token，禁止超时 Worker 的旧结果覆盖新结果。
- Coordinator 中断期间，API/任务不能继续提供全功能。恢复后通过持久化状态和租约恢复。**这是横向扩展计算层，不是多活高可用。**
- 文件都经 Coordinator 时，它也是网络吞吐瓶颈；要求测量吞吐及磁盘 I/O，记录未来共享存储/外部存储升级触发条件。

### 3.3 Docker 方案

- `docker/` 放 CPU 基础镜像与可选 GPU 镜像，模型/运行时版本固定，不使用不可复现的漂移标签进行最终交付。
- `deploy/compose.cpu.yaml`：CPU-only + API + Coordinator + Worker；只映射必要端口；SQLite/data 仅 Coordinator 持有。
- `deploy/compose.gpu.yaml`：可选 GPU profile；NVIDIA Container Toolkit / Docker GPU support 按官方环境要求检查；无 GPU 时普通服务不受影响。
- `deploy/compose.cluster-lite.yaml` 或明确的跨机器连接说明；全部通过内网且需内部认证，不暴露 Coordinator 管理 API 到公网。
- 根目录仅需要一个数据卷，模型缓存单独可选挂载；应用容器尽量以非 root 运行。

---

## 4. 文件与任务生命周期（必须先确定）

### 4.1 简化提交与结果 API（PoC 契约）

| 方法与路径 | 功能 | 核心语义 |
|---|---|---|
| `POST /v1/jobs` | `multipart/form-data` 上传+建任务 | 流式写入，校验配额，返回 202 + job_id；支持幂等键 |
| `GET /v1/jobs/{id}` | 查询任务 | 处理状态、进度、保留期、可用结果、引擎版本 |
| `GET /v1/jobs/{id}/events` | SSE | 持续事件，可断线重连；最终状态以查询为准 |
| `GET /v1/jobs/{id}/result?format=markdown` | 下载结果 | 只读、可重复读取，Content-Length/ETag 能确定则返回 |
| `POST /v1/jobs/{id}/ack` | 确认释放 | 显式确认完整消费，阻止后续新下载，转入异步清理 |
| `POST /v1/jobs/{id}/cancel` | 取消任务 | 尽力终止并清理；重复取消幂等 |
| `POST /v1/parse` | 可选同步解析 | 小文件限定上限、超时后返回明确错误/转异步，临时资源回收 |
| `GET /healthz`/`GET /readyz` | 探活/就绪 | 避免把数据盘/协调器不可用误报为就绪 |

上传和建任务一体化可减少首版 API；以后可添加预签名/分片上传接口，但**不应为了分片先引入对象存储**。

`POST /v1/jobs` 返回示例：

```json
{
  "job_id": "job_01...",
  "status": "queued",
  "created_at": "2026-10-29T12:00:00Z",
  "result_expires_at": null,
  "status_url": "/v1/jobs/job_01..."
}
```

ACK 示例：

```http
POST /v1/jobs/job_01.../ack
Authorization: Bearer ...
Idempotency-Key: ack-client-operation-01
Content-Type: application/json

{"result_version":"sha256:...","action":"release"}
```

服务返回 `202 Accepted` 和 `retention_status=release_pending`，表示**接受清理意图而非已经删净**。客户端可以随后查询到 `released`；在保留简短的 tombstone 期间，已释放/过期结果返回 `410 Gone`。未授权用户应视鉴权策略返回 `403` 或 `404`，不得泄露其他租户是否存在该任务。

**所有权约束**：一个 job 对应一个逻辑结果所有者；只有其授权调用方能发送 ACK。多消费者共用同一个结果的场景必须先定义引用计数/订阅方确认协议，第一版不做。

### 4.2 双维状态机，不要把所有状态硬塞一列

**处理状态**：`queued → running → completed | failed | cancelled`。

**保留状态**：`active → release_pending → deleting → released`，或 `active → expiry_pending → deleting → expired`。

对仍在运行、超时、取消的任务设置独立 `max_job_lifetime`；已完成结果的 TTL 从可用结果成功提交（commit）时开始计时，不以首次上传时间替代；此外允许绝对最长寿命，以避免无限续租。

**清理一致性**：

1. 原始输入文件写到 `.part`，校验大小/哈希，通过原子 rename 发布；结果先写临时位置，完成 fsync/校验/manifest 后提交任务完成状态。
2. ACK 或 TTL 先在 SQLite 中原子标记 `release_pending` / `expiry_pending`，此后拒绝新的下载租约。
3. 已开始的下载持有带期限的使用租约（可续租），清理器在租约结束前不能删除文件；下载中断时租约最终过期。
4. 清理器逐项幂等删除输入、工作文件、结果；目录删除成功后更新状态；SQLite 记录可保留有限时间作为 tombstone，再清理元数据。
5. Coordinator/清理器任意时刻崩溃，重启须扫描未完成的清理任务和孤儿 `.part`，重复操作不破坏其他 Job。

**默认参数草案**：结果 TTL `1h`、最大允许结果 TTL `24h`、最大任务寿命 `24h`、清理周期 `60s`、失败结果保留 `10m`、下载租约 `60s` 持续续租。参数需经过压测/故障测试后定稿，且一切过期时间使用绝对 UTC 时间保存。

### 4.3 ACK 失败场景必须覆盖

- 下载 HTTP 200 结束但客户端应用尚未成功处理：**不能自动 ACK**。
- SDK 提供 `ack()`，可选 `download_and_ack()`；仅当完整读取与用户指定的目标消费步骤成功才自动确认。
- ACK 后网络断线：重试 ACK 幂等；不会再次解析或误删其他 Job。
- ACK 在下载进行中：拒绝新下载，待进行中租约完成/过期后回收。
- ACK 和 TTL 同时触发：仅有一个最终清理路径，按持久化状态决定释放原因。
- 重复下载与重复 ACK：在 ACK 前允许下载多次；ACK 后结果不可再新取。
- 重启或进程被 `SIGKILL`：恢复 pending 删除，不能泄露永久占用文件。

---

## 5. SQLite 与轻量任务队列设计

建议 SQLite WAL + busy_timeout + 短事务，避免在事务内部执行文件 I/O、HTTP 调用或模型推理。**SQLite 文件只能由 Coordinator 所属主机访问**。

最小实体：

- `jobs`：`id, owner_id, input_name, mime_type, input_sha256, processing_status, retention_status, priority, requested_mode, requested_device, created_at, started_at, completed_at, result_expires_at, max_lifetime_at, current_attempt, result_version, result_manifest_path, error_code, updated_at`。
- `attempts`：`job_id, attempt_no, worker_id, lease_token, lease_until, started_at, last_heartbeat_at, state, error`。
- `artifacts`：`job_id, kind, path, size, sha256, generation, ready_at`。
- `download_leases`：`lease_id, job_id, expires_at, last_renewed_at`。
- `job_events`（有界，必要时）：`event_id, job_id, type, payload, created_at`；可支持 SSE 的 Last-Event-ID 断线恢复。

优先以最简事务建模：

1. Worker `claim` 原子挑选可运行任务，写入新的 `lease_token/lease_until`。
2. 心跳仅在 `job_id + lease_token` 匹配时成功；超时任务可按退避策略重新领取。
3. 成功提交结果时必须以 `lease_token` 做 fencing check；旧 Worker 的迟到提交必须拒绝。
4. 每 Job 设置重试上限，区分可重试（Worker 崩溃/临时网络故障）和不可重试（损坏/密码加密/格式不支持/配额不足）。
5. 进度更新节流（比如每秒或每若干页），不要高频写 SQLite。
6. 单个 Coordinator 是单点及写放大风险；应检查 WAL checkpoint、磁盘空间、DB 文件备份/清理策略和恢复演练。

最低可靠性目标：进程重启后保留所有已提交任务元数据；Worker 被杀死不会导致同一任务永久占有租约；完成与删除均具备幂等性；过期输出不能被旧 Worker 重新发布。

---

## 6. 内部 Worker 协议草案

Worker 必须注册能力：`worker_id`, `engines[{name,version,modes}]`, `device_type`, `gpu_model`, `gpu_memory_bytes`, `cpu_cores`, `memory_bytes`, `max_concurrency`, `heartbeat_at`。

内部 API 逻辑（路径可调整，须与公共 API 隔离）:

- `POST /internal/workers/register`：声明能力、获取短期会话。
- `POST /internal/tasks/claim`：领取任务并获得 attempt/lease token。
- `GET /internal/tasks/{id}/input`：受控流式读取输入；传输校验哈希。
- `POST /internal/tasks/{id}/heartbeat`：续租、进度上报。
- `PUT /internal/tasks/{id}/result`：流式回传结果包；使用 attempt token 原子提交。
- `POST /internal/tasks/{id}/fail`：分类报告失败。

要求：内网接口鉴权或 mTLS、读写超时、有限并发、按任务令牌授权文件读取、避免日志泄露文件内容；Coordinator 不做整文件内存缓冲。Worker 的临时工作目录仅持有执行副本，任务完毕尽快删除。

**路由**：从文件特征、`mode`、`device`、所需引擎、候选 Worker 负载选择；被用户强制要求 GPU 的任务不能在 CPU-only Worker 执行。为实现可预测质量，记录实际选择理由 `routing_reason` 与是否 fallback。

---

## 7. Document IR（中间表示）v0.1

不要把 Markdown 作为唯一的真相。定义内部 Document IR，然后导出 Markdown/Text/JSON。内部类型至少包括：

- `document`：`schema_version`, `source`, `parser_engine`, `parser_version`, `pages/sheets`, `elements`, `warnings`, `metrics`。
- `element`：唯一 ID、类型（标题、段落、列表、表格、图片、公式等）、文本、层级、阅读顺序、可空 `confidence`、来源位置。
- `source_location`：PDF 页码 + 带坐标系声明的 bbox，或工作表名称 + 单元格范围；不要混用不同坐标系。
- `table`：行列、单元格内容、行列合并信息、标题行、来源页/工作表、公式与缓存值（能提取时）。
- `image_ref`：如果需要图片，使用任务内 artifact 引用，不将大型二进制直接塞入公共 JSON。
- `warnings`：OCR 未运行、受保护文件、超出页数限制、转换降级、部分内容跳过等。

必须验证：Excel 多工作表、公式、合并单元格、数字精度；CSV 编码/分隔符/大文件截断；PDF 阅读顺序、页码与标题层级；扫描 PDF/OCR 文本是否具备可靠的定位和置信度信息。缺失元数据应显式为空，不可伪造精确坐标或 confidence。

---

## 8. Benchmark 设计（核心交付）

### 8.1 样本集

建议 **120–200 份** 不含隐私的有代表性文件，至少覆盖：

1. 中文/英文原生 PDF：单栏、双栏、长文档、含目录/脚注/页眉页脚。
2. 扫描 PDF：200/300 DPI、倾斜、模糊、中文英文混排、图片背景。
3. 表格 PDF：简单表格、跨页表格、合并单元格、财报数字表格。
4. 公式/图表 PDF：公式、脚注、图片内文字。
5. DOCX：标题层级、列表、表格、页眉页脚、嵌入图片。
6. XLSX/CSV：多个 Sheet、公式、日期与数字、合并单元格、GB18030/UTF-8、不同分隔符。
7. PNG/JPEG/WebP：简短截图、文档扫描、低清图像。
8. 异常：零字节、损坏、错误扩展名、密码保护 PDF、极大页数、小体积高膨胀率压缩文件（安全控制）。

每类保留人工校验的参考结果；先选 30–50 份建立高质量 Gold Set，其余作为吞吐和稳定性集。每份记录来源/许可、SHA-256、页数/大小、预期元素和复杂度，不直接提交敏感文档。

### 8.2 评估维度

| 指标 | 方法 | 说明 |
|---|---|---|
| 完成率 | 成功 / 总数，分格式记录 | 成功不等于输出正确 |
| 延迟 | 冷启动、热启动 P50/P95、总时间、页/s | 使用相同硬件与输入 |
| 吞吐 | 并发 1/2/4/8，文档/s、页/s | 控制资源占用和队列等待 |
| 资源 | 峰值 RSS、CPU、GPU VRAM、磁盘临时峰值、镜像体积 | GPU 显存应分引擎记录 |
| OCR 质量 | CER/WER，中文/英文分别计算 | 对齐人工文本，保留标注 |
| 表格质量 | 单元格内容准确度、行列/合并结构还原，可选 TEDS | Markdown 好看不代表结构正确 |
| 结构质量 | 标题层级、阅读顺序、页码、bbox/Sheet 坐标 | 人工标注的关键元素 |
| 稳定性 | 超时、崩溃、内存溢出、错误码、重试成功率 | 分类统计 |

### 8.3 实验纪律

- 固定文档文件哈希、引擎 git commit/release、依赖版本、模型版本、镜像 digest 和配置。
- 单引擎先预热，区分冷启动/首次模型加载与热调用；每样本正式运行 5 次，取中位数，并保留原始记录；如遇波动大，增加重复次数。
- CPU 对比固定 CPU 核数、线程数和内存限制；GPU 单独成组，不用 GPU 结果直接与 CPU 结果宣称框架优劣。
- 同一质量等级、相同 OCR 开关/语言、表格处理策略下进行主对比；单独记录各工具默认行为，避免错误对等。
- 数据表至少包含 `engine,version,config_hash,file_hash,format,pages,mode,device,run_id,elapsed_ms,peak_rss_mb,peak_vram_mb,status,output_hash,score`。
- 可参考官方基准，但**只采用本地可复现结果作最终选型证据**。
- 建立 `benchmark/results/raw/*.jsonl`、`benchmark/results/summary.csv`、`docs/ENGINE-BENCHMARK.md` 与可重跑脚本。

### 8.4 选型决策

实施前先约定加权评分：**质量 40%、性能 25%、资源 15%、格式覆盖 10%、运维/许可 10%**。具体阈值必须依据代表性的实际样本设定，而非凭空宣布“每页 0.1 秒”之类的硬指标。

P0 硬门槛：许可证适配、核心格式可用、CPU-only 可运行、容器可复现、异常文件不能无限占用资源。通过硬门槛后再算综合评分。

---

## 9. 安全与资源隔离验证

- 只允许白名单类型，联合检查扩展名/MIME/文件魔数；不要信任 HTTP Content-Type。
- 限制单次上传体积、PDF 页数、解压比、嵌套深度、任务耗时、单租户并发、磁盘容量、单文件输出上限。
- 文件名服务端生成，不拼接用户路径；工作目录权限隔离；解析子进程或容器限制文件系统、CPU/内存/PID/网络权限。
- 默认不支持远程 URL 抓取，不执行 Office 宏或文档脚本，不允许解析器不必要地联网。
- 如果有 Webhook：服务端签名（如 HMAC）、时间戳、投递 ID、重试/去重、回调地址和内网 IP 防护。
- 下载、状态、ACK 均校验任务 owner；日志不记录文件原文、token 或大段结果。
- Coordinator 磁盘满时：先回收已过期/已 ACK 资源，然后拒绝新上传并返回明确的可重试状态；不能提前删除仍在保留期内的有效结果。

---

## 10. 分步执行计划与验收

### P0-A：环境、资料与许可证核验

**工作**：只读检查 OS/架构/CPU/内存/磁盘、Docker 和 Compose、GPU 驱动、NVIDIA Container Toolkit 可用性；记录隔离目录和网络端口；锁定候选引擎版本与许可证；确定 Gold Set 结构。

**命令示例**：

```bash
uname -a
nproc
free -h
df -h
docker version
docker compose version
command -v nvidia-smi >/dev/null && nvidia-smi || true
```

**交付**：`docs/ENVIRONMENT.md`、`docs/LICENSE-REVIEW.md`、`docs/ENGINE-CANDIDATES.md`。

**门槛**：明确测试机配置，隔离目录和端口；每个候选都有版本、许可证和可复现启动入口。

### P0-B：引擎独立验证

**工作**：每个候选先单独 Docker/CLI/HTTP 启动，在小样本集验证功能与质量，再扩大到 Benchmark 全集；至少保证 Xberg、docling.rs、Docling 有明确结果；MinerU 按依赖/授权条件验证。

**交付**：`benchmark/`, `docs/ENGINE-BENCHMARK.md`, `docs/ENGINE-DECISION.md`。

**门槛**：每个候选清楚记录支持/失败的格式、失败案例、版本、速度、精度及资源；有分格式决策结论。

### P0-C：API + ACK/TTL 最小骨架

**工作**：Go API + Coordinator + SQLite + 本地数据目录；先用一个模拟解析器（可控延迟/失败）测试任务状态与文件生命周期，再接入一个 CPU 解析器。

**交付**：OpenAPI、最小实现、SQLite migration、`docs/JOB-LIFECYCLE.md` 与集成测试。

**门槛**：上传→排队→执行→结果→ACK→文件回收；无 ACK→TTL→回收；重启可恢复；重复 ACK 不破坏其他任务。

### P0-D：单机多副本与跨机最小闭环

**工作**：同机 2 个 API 副本，共用 Coordinator；通过负载均衡对同一 Job 交错调用；远端 Worker 通过内部 API 下载输入和上传结果；模拟 Worker 被杀死并重试。

**交付**：`docs/CLUSTER-LITE.md`、内部协议、Compose 文件、故障测试结果。

**门槛**：无状态 API 无粘滞会话；任何副本均能查询/下载/ACK；过期租约可恢复；旧 token 的结果提交被拒绝。

### P0-E：CPU/GPU 和容器验证

**工作**：CPU-only 全流程；GPU 可用则测试容器看到 GPU、模型加载、实际显存变化、质量/吞吐收益；GPU 不可用则给出可复跑脚本和阻塞报告。

**交付**：`docs/DEPLOYMENT.md`、CPU/GPU Compose、镜像版本清单、`docs/history/GPU-REPORT.md`。

**门槛**：无 GPU 机器能运行 CPU 版本；GPU 可选且不污染 CPU 镜像依赖；gpu_required 不静默降级。

### P0-F：决策收敛与实施任务书

**工作**：生成 ADR；选定首版引擎组合、默认策略、Document IR v0.1、公共 OpenAPI v0.1、SDK 生成方式、容器与清理策略；输出面向真正开发阶段的可执行任务拆分和测试验收。

**交付**：`docs/ARCHITECTURE.md`、`docs/DOCUMENT-IR.md`、`docs/DECISIONS.md`、`docs/IMPLEMENTATION-PLAN.md`。

**门槛**：无待猜测的核心协议语义；有明确的技术取舍、性能结果、风险、退出条件与下一阶段工作切片。

---

## 11. 最低必须自动化的验收用例

| ID | 场景 | 必须观察到的结果 |
|---|---|---|
| T01 | 正常上传并解析 PDF | 任务完成、结果可读取、格式/来源正确 |
| T02 | 正常上传 DOCX/XLSX/CSV/图片 | 核心格式能处理或返回明确的 unsupported 错误 |
| T03 | 结果下载后 ACK | ACK 返回接受；旧结果不可再新下载；清理完成 |
| T04 | 结果从未 ACK | TTL 到期自动删除，返回 410（在 tombstone 期内） |
| T05 | 下载过程中发生 ACK | 已租约保护的流按约定结束，清理不提前删除 |
| T06 | 两次重复 ACK | 幂等处理，无其他 Job 被影响 |
| T07 | ACK 与 TTL 并发 | 只有一个最终清理结果，无孤儿文件 |
| T08 | Coordinator 清理中 SIGKILL | 重启可继续清理，最终无孤儿数据 |
| T09 | Worker 执行中 SIGKILL | 租约到期、可重试；旧 token 无权提交 |
| T10 | 多 API 副本切换 | 同一 Job 在任意副本能查询/下载/ACK |
| T11 | CPU-only 节点 | 不依赖 GPU 或 CUDA 服务启动、解析 |
| T12 | GPU Worker 不存在且要求 GPU | 明确等待或能力不可用，不静默 CPU fallback |
| T13 | 非法文件/超限 | 拒绝并记录可读错误，不崩溃、不卡死 |
| T14 | 磁盘剩余空间低于安全水位 | 新请求受限，已有有效结果不会被非法抢删 |
| T15 | 旧任务事件/状态重复上报 | fencing 与版本约束拒绝过期写入 |
| T16 | 文件已释放或过期 | 有权限调用方在 tombstone 期获得明确 410 |

可用模拟文件/可控解析 Worker 完成生命周期验证，不必每个故障测试都启动真实 OCR 模型。额外记录真实引擎情况下的 T01/T02/T11/T12。

---

## 12. 建议仓库骨架

```text
.
├── README.md
├── go.mod
├── cmd/docparse/
├── internal/
│   ├── api/
│   ├── coordinator/
│   ├── jobs/
│   ├── storage/
│   ├── worker/
│   ├── engines/
│   └── documentir/
├── api/openapi.yaml
├── migrations/
├── benchmark/
│   ├── fixtures/README.md
│   ├── scripts/
│   └── results/{raw,summary}/
├── deploy/
│   ├── compose.cpu.yaml
│   └── compose.gpu.yaml
├── docker/
├── tests/{contract,integration,chaos}/
└── docs/
    ├── ENVIRONMENT.md
    ├── ENGINE-CANDIDATES.md
    ├── LICENSE-REVIEW.md
    ├── ENGINE-BENCHMARK.md
    ├── ARCHITECTURE.md
    ├── DOCUMENT-IR.md
    ├── JOB-LIFECYCLE.md
    ├── CLUSTER-LITE.md
    ├── GPU-REPORT.md
    ├── DEPLOYMENT.md
    ├── DECISIONS.md
    └── IMPLEMENTATION-PLAN.md
```

建议先用小而完整的目录实现核心闭环，不要因为目标骨架列出全部文件就提前造出大量空包。

---

## 13. 阶段最终汇报模板

Pi 在 Phase 0 结束时按以下结构回答：

1. **环境与版本**：CPU/RAM/OS/Docker/GPU、所用 commit/tag/镜像 digest。
2. **候选引擎结果**：格式覆盖、质量、性能、资源、许可证与失败案例。
3. **确定的技术选择**：首版解析器、备选/淘汰引擎及理由。
4. **架构与协议**：Standalone、Cluster-lite、ACK/TTL、文件下载租约、SQLite 任务队列、Worker token。
5. **验收测试结果**：T01–T16 的 PASS/FAIL/SKIP，跳过理由和证据。
6. **风险与未决事项**：Coordinator 单点、文件带宽、OCR/GPU 模型运维、扩容瓶颈。
7. **下一阶段工程任务**：按独立 PR 可实施的切片，列每个 PR 的接口、测试、完成定义。

**禁止只给“建议使用某项目”的主观结论，必须提供复现材料和原始测试结果。**

---

## 14. 一手参考资料（核对日期：2026-10-29）

- Docling Python / 官方服务文档：https://github.com/docling-project/docling ； https://github.com/docling-project/docling-serve/blob/main/docs/usage.md
- docling.rs 官方仓库及 PDF 一致性报告：https://github.com/docling-project/docling.rs ； https://github.com/docling-project/docling.rs/blob/master/docs/PDF_CONFORMANCE.md
- Xberg 官方仓库与文档：https://github.com/xberg-io/xberg ； https://docs.xberg.io/
- MinerU 官方仓库/档位与迁移：https://github.com/opendatalab/MinerU ； https://opendatalab.github.io/MinerU/zh/reference/migration_4_1/
- Apache Tika：https://tika.apache.org/
- SQLite WAL 官方限制：https://sqlite.org/wal.html ； https://www.sqlite.org/useovernet.html
- Docker Compose GPU / Profiles：https://docs.docker.com/compose/how-tos/gpu-support/ ； https://docs.docker.com/compose/how-tos/profiles/
- OWASP File Upload Cheat Sheet：https://cheatsheetseries.owasp.org/cheatsheets/File_Upload_Cheat_Sheet.html
- OpenAPI Generator：https://openapi-generator.tech/

> 注：开源仓库可能持续更新，Pi 必须在开始实验前记录被测试的精确版本。上述项目的公开性能数据仅用于确定值得测试的候选，不直接作为选型结论。

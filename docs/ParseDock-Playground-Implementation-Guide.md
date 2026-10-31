# ParseDock Playground — Web Frontend Implementation Guide (v1.0)

> 目的：提供一套可直接交给本地 Pi 执行的产品与工程任务书。  
> 范围：在**不改动现有 ParseDock 解析核心与公共 API** 的前提下，实现可运行、可测试、可 Docker 部署的「上传文件 → 解析 → 原文与 Markdown 对照」Web Playground。  
> 基线：用户提供的 Pi `PLAYGROUND-READINESS` 摘要及现有 Phase 0/PR-00 设计；**动手前必须核对仓库最新代码和 `docs/PLAYGROUND-READINESS.md`，实际实现以仓库当前公开 OpenAPI 为准**。

## 0. 本任务的完成定义

用户在内部受控网络中打开网页，即可：

1. 选择/拖拽本地文件并选择 `auto / fast / accurate`。
2. 通过同源 `/v1/` 代理上传，收到 job 后看到真实的排队、运行、成功/失败状态；SSE 优先、轮询兜底。
3. 左侧预览**同一份浏览器 File/Blob** 原文（首版必须支持 PDF、PNG/JPEG/WebP、TXT/MD、CSV/TSV），右侧看到从后端结果接口获取的 Markdown；支持「渲染 / 源码」、复制、下载 `.md`。
4. 可主动取消未完成任务；完成后可显式 ACK 释放**服务端**结果；未 ACK 时依靠既有 TTL 清理；410/404 有清晰提示。
5. 不暴露 API Key，不引入第三方文件上传、对象存储、账号数据库、额外队列；随现有 CPU Compose 可启动；有单元、集成、Playwright E2E、视觉和安全测试。
6. 第二阶段支持 DOCX、XLSX/XLSM 的**尽力预览**；PPTX/旧 DOC/XLS 只显示文件信息与不支持预览说明，不影响解析。

明确不做：在线编辑文档、像素级 Office 还原、PDF 与 Markdown 智能逐行同步、高亮 OCR bbox、登录/多租户控制台、分享链接、服务端原件下载、解析引擎参数面板、完整历史记录、复杂后台管理页面。*不得因为有这些预留需求而扩大首版范围*。

## 1. 项目与部署原则（硬约束）

- 后端保持：Go + SQLite + Coordinator + Worker，API 无状态；解析输入与输出受 ACK/TTL 管理。
- 前端：React + TypeScript + Vite；UI 推荐 Tailwind CSS + Radix primitives；可调整分栏 `react-resizable-panels`；异步请求 TanStack Query；Markdown `react-markdown` + `remark-gfm` + `rehype-sanitize`，不启用 `rehype-raw`；E2E Playwright。
- 优先按现有工程的包管理器、ESLint/formatter、CI 习惯实施。引入新包时校验活跃维护、许可证、bundle 体积和安全；固定版本与 lockfile，不写未核验的 `latest`。
- **不修改 ParseDock 公共 API 以适配 UI**；先确认已有字段。发现真实阻断时独立记录，最小改动需另行说明、测试、决策。
- 后端现有 Java/Python SDK 不应直接塞进浏览器。使用 `api/openapi.yaml` 生成 TypeScript **类型**，再编写轻量浏览器调用层；严禁使用 `api/openapi.internal.yaml` 生成客户端。
- 不更改 Worker 镜像许可证发布门禁；当前 Xberg CPU Worker 镜像 GPL 附属库问题仍未解除，不得因新增前端而绕过或自动对外发布 Worker。
- 不启动对公网公开的 Playground；默认仅限 localhost / 内网 / VPN / 身份保护的入口。现阶段是**内部单逻辑 Owner 工具**，不是公开 SaaS。

## 2. 产品交互与 UI 结构

### 2.1 单页信息架构

顶栏：左侧 ParseDock Playground 标识；右侧后端可用性、当前引擎/模式（仅结果可知时显示）、简短帮助。  
上传区：文件拖拽、点击选取，文件名/大小/类型，解析模式切换，`解析` 按钮；选择新文件时提醒原任务未 ACK 将在 TTL 回收。  
工作区：左右分栏，支持拖动比例，默认约 50/50：

- **左栏：原始文件**。有文件格式标签、页面/文件信息。PDF 用浏览器 iframe/object `blob:` 预览（不保证跨浏览器能力一致）；图片原生 `img`（适配缩放）；纯文本/Markdown/CSV/TSV 以安全文本形式显示；Office 第二阶段添加对应库。
- **右栏：解析结果**。`预览`/`Markdown 源码` 切换；复制、下载；显示引擎、实际设备、解析耗时（如 API 返回）、警告摘要；数据只来自**真实下载结果**。
- 底部状态区：排队 / 解析 / 完成 / 错误 / 已释放，进度不伪造；`取消任务` 和 `释放服务端文件` 分别在合法状态显示。

### 2.2 UX 状态机

`idle → file_selected → uploading → queued/running → completed → releasing → released`；可进入 `error / cancelled / expired`。前端 UI 状态不能替代后端任务状态；页面刷新后本地 File/Blob 丢失，不能假装还能显示原文；要明确说明。

- 只在从后端获取并完整解码 Markdown 后启用复制/下载。
- **绝不自动 ACK**：不能在 HTTP 200、组件 unmount、页面关闭、下载按钮点击或文件切换时触发释放。
- 用户显式点击「释放服务端文件」才调用 `/ack`，使用 **`X-Result-Version`**，不得拿 ETag 代替；202 是意图接受，仍可显示 `release_pending`，最终由状态接口确认。
- ACK 后本地已经加载的 Markdown 可以继续显示，须清楚标记「服务端结果已释放，新下载不可用」。
- `POST /v1/parse` 是 one-shot 同步语义，不应用作这版 Playground 主流程；**统一用异步 job API**，以保持状态、SSE、ACK 一致。
- 原文件预览优先使用浏览器本地 `File`，不增加 `GET /input`；需要 `URL.revokeObjectURL`、取消过期异步操作、防止竞态覆盖新文件。
- `mode=accurate` 文案不承诺始终文本更准确，可用辅助提示「优先结构与来源信息；结果因格式而异」。

### 2.3 视觉与响应式验收

- 不套通用 admin dashboard 模板；参考专业开发者工具的简洁工作台风格：清晰层级、克制色彩、可读的长内容。
- 桌面 ≥ 1024px：左右分栏、拖拽调整；窄屏：上下堆叠或标签切换，功能不丢失。
- 完整处理空白态、上传中、长文件、错误、无预览、过期和已释放状态；加载与错误提示不遮蔽主要内容。
- 键盘可完成文件选择、模式切换、按钮操作；明确焦点样式；对拖拽增加非拖拽替代路径。图标有标签，颜色不作为唯一状态信号。
- Markdown 长表格横向滚动、代码块可读；不渲染不可信 HTML。
- Playwright 保留 1440px 桌面、窄屏、长 Markdown 等代表场景的截图供人工视觉验收；不止依靠 snapshot 测试通过。

## 3. 文件格式预览策略

| 格式 | P1/MVP 行为 | 技术 | 边界 |
|---|---|---|---|
| PDF | 必做，左侧预览 | `blob:` + iframe/object | 浏览器差异；失败给下载/文件信息兜底；不声称支持页码联动 |
| PNG/JPEG/WebP | 必做，原生图片预览 | Blob URL + `<img>` | 限制像素/文件大小，及时释放 URL |
| TXT/MD | 必做，纯文本安全显示 | File.text / TextDecoder | 大文件截断提示，保留编码异常提示 |
| CSV/TSV | 必做，文本或轻量表格预览 | PapaParse（可选 worker/chunk） | 仅预览前 N 行/列，非完整表格编辑器 |
| DOCX | 第二阶段，尽力预览 | `docx-preview`，必要时隔离容器 | 不保证版式/分页与 Word 一致；不执行宏 |
| XLSX/XLSM | 第二阶段，表格预览 | `exceljs` 或经过对比确定的库 | 限制文件大小、行列与 sheet 数；不执行公式/宏；巨大文件退化为文件信息 |
| PPTX、DOC、XLS | 第一版只显示文件信息 | 不加载重型转换器 | 后端仍可解析（如果支持） |
| HTML | 第一版只以**文本**显示或不预览 | 不直接执行 HTML | 不运行用户脚本；不要把 `allow-scripts` 与 `allow-same-origin` 组合 |

所有表格与 Office 预览不执行文件中活动内容，不主动请求其外部 URL。遇到不受控资源引用时拒绝/替换。文件很大时不做 `readAsText`/`ArrayBuffer` 全量内存解析，按大小阈值降级展示信息与提示。

## 4. 浏览器、代理和鉴权（安全 P0）

后端当前没有 CORS、没有静态文件服务。**本版不修改后端以加 CORS**；开发环境使用 Vite dev proxy，生产使用独立静态 Web 容器 + Nginx 同源 `/v1/` 反向代理。

```text
Browser → https://playground.example.internal/
       ├── /              static SPA
       └── /v1/*         Nginx same-origin proxy → ParseDock public API replicas
                                                   (no /internal forwarding)
```

- `EventSource` 不支持普通自定义 Authorization header；代理在服务端为 `/v1/*` 注入已有后端所需的 Bearer Key。密钥来自**运行时 secret 文件/受保护的进程配置**，绝不以 `VITE_*`、源码、`dist/`、HTML、JS、Source Map、公开响应或日志的形式暴露。
- **极重要**：代理隐藏了 key 不等于用户被认证。任何能访问该代理的人都能使用共享 Owner 的解析额度和文件生命周期 API。默认只能部署在可信的内网/VPN/身份认证网关之后；如需公网开放，必须先设计独立访问认证、隔离/配额、CSRF/滥用防护，**不能仅靠一个共享代理 key 开放公网**。
- 同源写操作应校验 `Origin` / Fetch Metadata（有适当缺省处理），结合要求自定义请求头、访问控制等防 CSRF/跨站滥用；不能仅依赖浏览器不返回跨站 CORS 结果。
- Nginx 仅代理明确的 `/v1/` 路径，不暴露 `/internal/*`；注意保留正确上传流、响应状态、超时和 Content-Length；SSE 要禁用代理缓冲、延长读取超时，透传事件 ID，避免 gzip/中间代理缓冲造成失效。
- 统一跨层 request ID；不打印 Authorization、文档正文、Markdown、原文件名里的敏感信息。代理限制上传 body、并发、速率，并保持与后端配置一致；64 MiB 是当前限制，128 MiB 仅在资源评估和端到端测试通过后作为可配置值开启。
- 安全响应头及 CSP：`default-src 'self'`、`connect-src 'self'`、必要的 `blob:` 预览与 worker 例外、限制 frame / object / img 来源；按实际组件验证，不能为了测试过关使用 `*` 兜底。启用 `X-Content-Type-Options: nosniff` 等；敏感内容 `Cache-Control: no-store`。
- Markdown 禁止加载远程图片、iframe、脚本、原生 HTML；可以仅显示远程图片占位，不能隐式发网络请求。对不可信链接使用安全 URL 策略与 `rel` 属性。

## 5. 后端 API 适配层（不得臆造接口）

先对照**当前** `api/openapi.yaml`、真实 handler、路由、测试，检查以下操作及响应字段：

| 操作 | 接口 | 关键行为 |
|---|---|---|
| 提交 | `POST /v1/jobs` multipart | `202` job，文件流式上传；支持 mode；正确处理 413/415/429/507 |
| 轮询 | `GET /v1/jobs/{id}` | 以处理/保留状态为准，不虚构百分比 |
| 事件 | `GET /v1/jobs/{id}/events?scope=processing` | snapshot + 后续事件；SSE 失败走有限间隔轮询；避免双通道重复更新 |
| 下载 | `GET /v1/jobs/{id}/result?format=markdown` | 只在 completed & active 获取；获取 `X-Result-Version` 并保留给 ACK |
| IR | `GET .../result?format=json` | 可选获取引擎、警告和其他元数据，缺字段时降级；避免不必要的大结果 |
| 释放 | `POST /v1/jobs/{id}/ack` | 只有用户显式确认；带正确 result_version；202 后查询释放进度 |
| 取消 | `POST /v1/jobs/{id}/cancel` | 只用于 queued/running；完成态不把 cancel 当 ACK |
| 健康 | `/healthz`, `/readyz` | UI 的状态信息，不能用健康探针替代真实 job 状态 |

使用 `openapi-typescript` 生成公共 API 类型，定制一个非常薄的 fetch 客户端（上传进度若无法可靠获得，不要把“已提交请求”标成精确上传百分比；可先展示上传中）。在浏览器不能直接读取/设置一些限制响应头时，需要由代理正确透传，按实际 E2E 验证。

SSE 事件竞争：只对**当前 job id 与请求序号**的事件更新状态；任务取消或用户选择新文件时关闭 EventSource、停止旧请求；断流后指数退避轮询，状态恢复后停止轮询；收到已完成/已释放终态需收敛；`Last-Event-ID` 或 cursor 恢复逻辑以服务器实际协议为准。

## 6. 数据管理、保密与性能

- 文件仅存在浏览器会话内存/File 引用；不写 localStorage、IndexedDB、第三方分析服务，默认不把 Markdown 发送到外部服务。
- 不存文件内容到生产日志、监控属性、异常追踪；不将敏感文档传给截图/日志归档 CI（测试只使用合成数据）。
- `URL.createObjectURL` 只对受支持文件使用并成对 revoke；取消异步任务与 FileReader；显著限制 Office/CSV 等前端加载的大文件，明确降级。
- 后端已有 ACK/TTL 与 410 tombstone。前端刷新、关页、断网**绝不自动 ACK**；没有确认就交给 TTL。用户本地 Markdown 下载是浏览器行为，不能证明其已存储，所以不自动 ACK。
- 不做服务端文件缓存、不创建任何 Playground 专用数据库、不增加 S3/Redis/Postgres。

## 7. 实施切片（可独立合并的 PR）

每片必须有单独测试、明确完成定义，遵循现有工作流；不得为了 PR 切分制造无效空 PR。建议顺序如下。

| 切片 | 核心内容 | DoD |
|---|---|---|
| PG-01 骨架 | `web/` React/TS/Vite、Tailwind、UI tokens、lint/typecheck/test/build、根目录脚本、CI 分类 | 本地开发能启动空壳；CI 可真实跑且锁文件固定 |
| PG-02 类型与代理开发 | `openapi-typescript` 仅公共 spec；fetch adapter；Vite same-origin proxy；无客户端密钥 | 类型漂移在 CI 失败；EventSource 对相同 origin 工作；dist 安全扫描通过 |
| PG-03 上传与任务 | 拖拽/选择，mode，异步创建、状态轮询、错误态、取消初步接入 | 用 fake API + 真实 CPU 后端完成一次 PDF job；错误场景可见 |
| PG-04 左栏原件 | PDF、图片、纯文本/CSV，Blob 生命周期，格式兜底，分栏拖动 | 预览文件类型正确；切换文件不泄漏 URL；大文件安全降级 |
| PG-05 Markdown 结果 | 右栏 Markdown/源码、GFM 表格、复制/下载、引擎与警告摘要 | 不加载外部资源、不执行 HTML/XSS，结果与 API 字节一致 |
| PG-06 SSE 进度 | SSE/轮询降级、重连、终态、取消竞态 | 测试断线、重复事件、旧 Job 串台、410/404 |
| PG-07 ACK/TTL | 显式 release、版本校验、release_pending、410、刷新说明 | 不自动 ACK；释放操作只影响当前 job；SDK/前端一致 |
| PG-08 Office 尝试性预览 | DOCX、XLSX/XLSM，限制大小/行列，错误降级 | 无脚本执行/远程资源访问；不支持格式有明确占位 |
| PG-09 Docker 部署 | 静态 Nginx 容器、同源反代、运行时密钥、Compose 可选 profile、文档 | CPU-only compose 可启动前端，其他服务不暴露；SSE 稳定；不发布受许可证阻断的 Worker |
| PG-10 完整 E2E/视觉/文档 | Playwright 真实服务集成+截图、a11y、可复现安装、README | CI 全绿，真实浏览器视觉验收；形成交付报告 |

**阶段里程碑**：MVP 由 PG-01～PG-07 + PG-09/10 构成，PG-08 为增强项。可在 PG-07 后先完成部署与 E2E，再补 Office；若时间/复杂度冲突，先保证完整可用闭环。

### 每片的验收用例最低要求

1. 上传小 PDF：状态成功、左侧预览、右侧 Markdown、复制/下载正确。
2. 图片：原图显示，解析 Markdown 真实来源，不能用固定 mock 伪装成功。
3. 文本/CSV：文件编码异常清楚提示，原文预览可读，服务端解析保留正确中文。
4. 上传 415/413/429/507、服务器 5xx、网络断线：有可操作错误提示，禁用不合法按钮。
5. 完成态 `X-Result-Version` 只作为 ACK token；`ETag` 用于表述内容缓存，不混用。
6. 不点击 release：不会请求 ACK；主动点击 release：202 → status → released/410。
7. SSE 掉线转轮询后最终能完成；更换文件后旧任务事件不能覆盖新结果。
8. XSS fixture（Markdown HTML、`javascript:`、外部图片 URL）：无脚本执行/无意外请求。
9. 浏览器控制台与打包 `dist/` 不出现 API Key；直接访问 `/internal` 不会成功被代理。
10. 移动/窄屏布局能操作；键盘可访问主要功能。
11. PDF、图片、本地文件预览在用户切换与卸载时清理 object URL。
12. 现有后端 Go 测试、契约测试、ACK/TTL、许可证门禁不回退。

## 8. 测试矩阵和 CI

- Unit：Vitest/Testing Library（如项目没有约定则优先选择），覆盖组件、格式分类、状态与结果处理、重试和 URL 回收。
- Contract：公共 OpenAPI → TS 类型漂移检测；GET/POST 模拟响应与状态码契约。
- E2E：Playwright；Mock server 测易错边界，真实 CPU Compose 测上传—结果—显式 ACK；Office 文件只用不敏感、可公开的测试样本。
- Visual：固定 viewport，人工查看截图、确认无错位/表格溢出/无效 hover/焦点丢失。截图不包含用户私人文档。
- Security：扫描构建产物中的测试密钥和可能意外暴露的 env；HTTP 代理测试 `/internal/*` 不可访问；测试跨站请求防护、外链资源不加载；日志无文件正文。
- 性能：初次 JS bundle 与 Office 分块/懒加载体积；大 PDF 只借浏览器内置预览，不做无界主线程加载；Office 大文件预览门限可配置。
- CI：每个相关 PR 执行 install, lint, typecheck, test, build, contract；E2E smoke 根据现有 runner 成本优化，完整可安排 nightly。保留当前 CI 的失败归总逻辑与已有门禁；不要因为私有仓库分支保护限制而宣称 GitHub 强制挡合并。

## 9. 建议代码布局（允许按现有仓库调整）

```text
web/
  src/
    app/                   # 组合页面、基础路由（可暂时只有一个路由）
    components/            # Dropzone / SourcePane / MarkdownPane / JobStatus
    features/playground/   # state/hooks/view models
    api/                   # generated public API types + thin client
    preview/               # PDF / Image / Text / CSV / Office adapters
    styles/
  e2e/
  public/
  package.json
  vite.config.ts
  playwright.config.ts
  Dockerfile

deploy/
  nginx.playground.conf / template
  compose.playground.yaml (or existing compose profile)

docs/
  PLAYGROUND-IMPLEMENTATION.md
  PLAYGROUND-TEST-REPORT.md
```

前端 JS 仅调用 `/v1/*`；所有鉴权发生在受保护的同源代理层。无需路由大框架、不提前引入全局复杂状态库。

## 10. 每阶段执行流程

1. 阅读 `docs/PLAYGROUND-READINESS.md`、当前 OpenAPI、Router/Handler、现有 CI 与 Compose。以实际代码确认字段、SSE framing、返回头、错误码及项目结构；将差异记录为 `docs/PLAYGROUND-IMPLEMENTATION.md` 的现状基线。
2. 建立短小 ADR：为何选择同源代理、internal-only 共享 Key、为何不自动 ACK、预览格式与边界、Office 是否后置。
3. 按 PG-01…10 实施，**每片开发→单测→本地集成→必要真实浏览器检查→PR→CI→复核**，不跳过绿灯门禁。任何改动不应对其他运行中的应用造成影响。
4. 每阶段有提交记录、变更摘要、运行命令、截图（合成/公开文件）、测试结果和未完成项。不要把 UI mock 的测试写成真实解析服务验收。
5. 完成 MVP 后执行真正的 Docker + 浏览器端到端验收：上传 PDF、左右对照、复制/下载、SSE、ACK、后续 410；确认 API Key 只在代理侧；再次跑原有 ParseDock 后端测试/CI。
6. 未解决的阻断标注 BLOCKED/PARTIAL，例如原始文档浏览器不支持预览、后端 OCR 语言无法配置、Worker 许可证未清、无法公开部署；不得臆造功能或绕过许可证门禁。

## 11. 最终交付与 DoD

- 可运行的 Playground Web App，用户完成一次完整的文件解析对照工作流。
- 可复用的公共 OpenAPI 类型层、请求封装、SSE 状态管理；无 backend core 侵入式修改。
- Docker/Compose 可选启动，部署说明包含 API key 安全注入、访问边界、上传限制和网络配置。
- 实测截图、Playwright E2E、单元/集成、安全测试及稳定 CI；关键流程负向测试存在。
- `docs/PLAYGROUND-IMPLEMENTATION.md`（实现与取舍）、`docs/PLAYGROUND-TEST-REPORT.md`（命令、原始证据、环境、通过/失败）、README 使用说明。
- 完整记录最终 PR、commit、CI run ID、known limitations、后续 backlog。

**禁止用纯静态 mock 或只做漂亮截图冒充完成；必须使用真实 ParseDock CPU 后端通过完整的上传→解析→对照→手动释放流程。**

# xLyra 插件宿主能力支持规范

本文档定义 xLyra 主程序（`server/internal/jsplugin`、`server/internal/gateway`、`server/internal/store`、`server/internal/admin` 及相关包）为插件系统提供宿主能力支持的设计规范与工程约束，适用于所有新增或演进的插件宿主能力。

本规范延续去 `kind` 化与声明式能力组合的演进路线：插件只描述业务决策，重计算、跨协议适配、凭据与明文隔离、流式状态机与安全兜底由宿主提供通用原语统一承接。

---

## 1. 定位与演进方向

xLyra 插件运行在受限的 `moejs` ES2023 沙箱中（无外部依赖、无网络访问、无文件系统、单次调用受严格超时与内存配额限制）。随着业务场景从“站点探测与协议适配”扩展到“请求/响应内容治理、双向脱敏、合规审核、术语映射、外挂推理服务联动”等场景，主程序必须在保持安全边界的同时提供更强的能力支撑。

演进的核心路径是：**主程序不针对某一个具体业务插件编写专用逻辑，而是将需求抽象为可复用的通用类、通用引擎和通用管线接口，让多类插件通过组合清单声明与标准钩子即可复用。**

---

## 2. 五大核心设计原则

### 2.1 通用原语优先，禁止为单一业务建垂类

主程序代码中**禁止**出现只服务于某一特定插件业务的概念硬编码：

- 不为单一业务新增专用插件 `kind`（例如不建 `kind: "redaction"`、`kind: "moderation"`）。
- 不为单一业务新建专用绑定表或专用存储字段。
- 不在 `internal/gateway` 中编写只服务于某个 `plugin_id` 或单一业务名称的专用响应过滤器、专用开关或专用错误码前缀。

### 2.2 三场景准入检验

任何能力在合入宿主前，必须通过**三场景检验**：用中立的通用命名与数据结构，在不修改宿主代码的前提下，至少能支撑 2–3 类不同业务插件。

| 待落地需求 | ❌ 专用化做法 | ✅ 通用原语做法 | 可复用的其他业务场景 |
| --- | --- | --- | --- |
| 按下游密钥对敏感字段双向脱敏 | 新增 `redaction` kind、`detectSensitive` 钩子、`redaction_failed` 错误码 | 提供 `gatewayPipeline` 声明段、`inspectSegments` 钩子、`payload.inspect_raw` 权限、`internal/textmatch` 匹配引擎与 `internal/textxform` 变换引擎 | 1. 敏感信息双向脱敏<br>2. 内容合规审核与拦截（`reject`）<br>3. Prompt 注入检测与标记<br>4. 企业专有名词 / 术语双向替换<br>5. 模型输出敏感词清洗 / 数据水印 |
| 接入自建中文 PII NER 模型 | 新增 `redaction.nerService` 配置 | 提供通用受管 `external_services` 注册表与 `entity: external_service`，插件声明所需 `serviceType` | 1. PII NER 实体识别服务<br>2. 外部文本风控 / 安全审核 API<br>3. 语义分类 / 路由打标服务 |

### 2.3 能力组合优先于新增 `kind`

- 原则上**不再新增业务 `kind`**。
- 插件通过「**清单能力段（Capability Section）+ 通用钩子（Hook）+ 通用权限（Permission）**」按需组合接入宿主。
- `xlyra-plugin init --template <name>`（及兼容别名 `--kind`）只用于生成起步脚手架，宿主内部通过清单声明与导出钩子自动装配运行时能力。

### 2.4 清晰的宿主 / 插件职责边界

| 职责归属 | 负责内容 | 原因 |
| --- | --- | --- |
| **宿主（Go）** | 跨 AI 协议正文抽取与原地回写、签名块保护（如 Anthropic `thinking`）、Go RE2 / Aho-Corasick / 加解密 / 哈希缓存、流式跨分片有界状态机、请求内双向映射会话、Fail-Closed 阻断与熔断、内网服务连接与凭据托管 | 性能可控（比 JS 解释器快 1–2 个数量级）、内存安全、多协议细节对插件透明、密钥/明文不越界 |
| **插件（JS + 清单）** | 声明类型体系、正则表达式/校验/上下文/词典/白名单配置、声明式变换策略与失败策略、无法用静态规则表达的上下文启发式裁决、误报过滤、自定义拒绝提示 | 业务逻辑灵活迭代、插件轻量打包分发（≤ 2 MiB）、沙箱内无状态纯函数易于测试与升级 |

### 2.5 安全与失败语义由通用策略声明驱动

敏感数据隔离、受信签名门禁、日志静默、诊断抓包禁用、失败时是跳过还是拒绝请求，全部由**清单中的通用权限位与策略枚举**驱动，严禁通过 `if plugin.ID == "..."` 或 `if kind == "..."` 做特判。

---

## 3. 宿主六大正交扩展轴

所有向宿主提出的新插件能力需求，必须先拆解并映射到以下六个正交扩展轴之一；若现有轴已满足则直接复用，若缺口确属通用能力则在该轴上做增量扩展。

| 扩展轴 | 宿主职责（通用抽象） | 仓库代码锚点 | 扩展约束 |
| --- | --- | --- | --- |
| **轴 1：作用域与对象绑定** | 清单驱动的绑定表单校验与渲染、内置实体注册表（`oauth_connection`、`api_key`、`site`、`external_service`）、互斥绑定约束（`exclusive`）、`Snapshot` 不可变内存索引 | `server/internal/jsplugin/entities.go`<br>`server/internal/jsplugin/automation_manage.go`<br>`server/internal/store/js_plugin_repository.go` | 新绑定复用 `js_plugin_bindings` 与 `js_plugin_binding_inputs`；按 `scope_type` 与 `slot` 区分作用域，禁止另起业务绑定表 |
| **轴 2：网关双向拦截管线** | 入口拦截（下游请求解码之后、模型映射与选路之前执行一次，生成请求级内存 `TransformSession`）+ 出口拦截（缓冲响应、Canonical 流、透传 SSE、WebSocket，带通道级跨分片缓冲） | `server/internal/gateway/handler_chat.go`<br>`server/internal/gateway/execution.go`<br>`server/internal/gateway/protocol_canonical_stream.go`<br>`server/internal/gateway/sse_sanitizer.go`<br>`server/internal/gateway/responses_websocket.go`<br>`server/internal/gateway/bridge_image_executor.go` | 入口只做一次，重试与故障切换共享结果；流式出口统一复用可组合的 `streamTextTransformer` 状态机，有严格待定字节上限 |
| **轴 3：跨协议文本片段编解码** | 统一维护下游端点白名单与字段抽取规则（`SegmentCodec`），将各协议体抽成带 `role`/`field`/`isJSONString` 元数据的 `Segment[]`，并支持原地无损回写 | `server/internal/gateway/endpoint_registry.go`<br>`server/internal/gateway/dsml_sanitizer.go` | 必须保护不可篡改/带签名的字段（如 Anthropic `thinking`/`redacted_thinking`、加密推理块、二进制媒体）；未声明端点由 `unsupportedEndpoint` 策略决定放行或拒绝 |
| **轴 4：宿主原生计算与匹配引擎** | 提供线性时间纯 Go 计算与匹配能力：RE2 规则（含捕获组、字符边界、内置格式校验器、Shannon 熵、上下文窗口）、Aho-Corasick 词典、白名单、片段内容哈希 LRU 缓存、占位符/替换/拦截变换器 | `server/internal/jsplugin/host.go`<br>`server/internal/jsplugin/host_crypto.go`<br>（及通用文本匹配/变换包） | 原生计算仅放高价值、重 CPU 或跨调用复用的原语；缓存只存无敏感文本的结构化偏移结果，缓存键须纳入影响插件判断的全部上下文维度 |
| **轴 5：受管外部扩展服务** | 管理员维护的外部 HTTP 扩展服务连接（名称、能力类型、URL、允许内网开关、Bearer 密钥托管、超时与并发信号量） | （通用外部服务模块及 `entities.go`） | 仅管理员可配置 URL 并决定是否允许私网地址；插件只能声明所需 `serviceType` 与请求能力，永远不能自行指定任意 URL |
| **轴 6：安全、失败策略、可观测与测试** | 权限静态互斥校验、受信签名门禁、`fail_open`/`fail_closed` 策略、热路径双池隔离与熔断、无明文聚合计数观测、通用 Dry-Run 预览与端到端 Fixture 引擎 | `server/internal/jsplugin/manager.go`<br>`server/internal/jsplugin/breaker.go`<br>`server/internal/jsplugin/limits.go`<br>`server/internal/jsplugin/selftest.go` | 高敏权限启用强制校验受信签名；任何错误日志、指标与持久化 metadata 均不得包含未脱敏片段文本 |

---

## 4. 权限分级、静态互斥与通用错误码标准

### 4.1 权限分级与启用门禁

除原有的 `storage`、`http`、`net:<host>`、`schedule`、`secret.use`、`event:<name>` 之外，宿主通过通用权限位控制敏感数据访问能力：

| 权限 | 含义 | 签名门禁 | 运行期附加保护 |
| --- | --- | --- | --- |
| `secret.use` | `signRequest` 可读取 `ctx.credential` 明文凭据 | **强制受信签名或市场证明**（`ConfirmUntrusted` 不可绕过） | 强制丢弃 `log.*`；错误字符串擦除秘密袋中的值 |
| `payload.inspect_raw` | 网关入口管线/钩子可检查未脱敏的请求原文片段 | **强制受信签名或市场证明**（`ConfirmUntrusted` 不可绕过）；启用确认框须显式提示“可读取所绑定密钥/范围的请求原文” | 强制 `WithDiscardLogs(ctx)`；禁止原始正文抓包与诊断持久化；错误信息禁止回显片段文本 |

### 4.2 高敏权限静态互斥规则

能够接触下游原文的插件不得拥有任何将原文外发或持久化的通道。在清单解析校验阶段（`validateManifest`），凡声明 `payload.inspect_raw` 的插件，宿主**直接拒绝加载/启用**以下组合：

- 权限清单中包含 `http`、`net:*`、`storage`、`schedule`。
- 清单包含 `automation` 段或导出 `handle`、`onEvent`、`onSchedule` 钩子。
- 同时声明 `siteProvider`、`protocol` 与 `gatewayPipeline` 互斥段（同一插件不得混合声明多个独立主能力段）。

### 4.3 通用失败策略与错误码

网关管线绑定通过通用枚举声明失败语义，错误码与 HTTP 状态码统一使用中性命名：

| 策略/条件 | 触发场景 | HTTP 状态码 | 错误码 (`code` / `error.type`) | 行为 |
| --- | --- | --- | --- | --- |
| `failurePolicy: "fail_open"`（默认） | 匹配/钩子/外部服务失败，或插件被停用/卸载/熔断 | 正常继续 | — | 跳过该管线，沿默认网关路径放行请求 |
| `failurePolicy: "fail_closed"` | 运行期规则、外部服务、`inspectSegments` 钩子超时/抛错/返回非法结构 | `503` | `plugin_pipeline_failed` | **阻断请求**，不发往上游，不降级放行 |
| `failurePolicy: "fail_closed"` | 绑定仍存在，但所属插件版本被停用、卸载或熔断器跳闸 | `503` | `plugin_pipeline_unavailable` | **保持绑定阻塞态并拒绝请求**，直到管理员恢复插件或显式解绑 |
| `unsupportedEndpoint: "reject"` | 绑定范围的请求命中不在宿主 `SegmentCodec` 白名单中的端点（含 `/v1/audio/speech`、`/v1/plugins/<slug>` 等） | `400` | `plugin_unsupported_endpoint` | 直接拒绝，不发往上游 |
| 扫描文本超限 | 待检查抽取片段总字节数超出管线配额（默认 8 MiB） | `413` | `plugin_payload_too_large` | 直接拒绝，不发往上游 |
| 钩子主动拦截 | `inspectSegments` 返回 `{ reject: { status?, code?, message? } }` | `400` / `403`（默认 `400`） | 插件指定的通用错误码（缺省 `plugin_payload_rejected`） | 直接拒绝，不发往上游 |
| 出口变换异常 | 流式/缓冲出口还原阶段宿主内部出错 | 保持响应 | 仅记内部监控指标 | 输出原占位符文本，确保绝不因还原异常泄露或截断为明文 |

---

## 5. 代码工程红线与准入评审 Checklist

### 5.1 仓库架构红线（由自动化测试守护）

1. **`moejs` 导入隔离**：除 `server/internal/jsplugin/engine.go` 外，任何文件不得导入 `github.com/Yachiiiiyo/moejs`（`runtime_test.go` 源审计测试会直接失败）。所有数据校验、匹配与解码算法写在纯 Go 文件中，仅在 `engine.go` 中完成 `moejs.Value` 编解码。
2. **热路径双池隔离与熔断**：在网关请求等待期间执行的钩子（如 `inspectSegments`）必须注册进 `isHotHookName`，自动路由至 `hotPool`（4/16 会话），并接入 `Breaker.RecordProtocolCall` 统计错误率与中断次数。
3. **存储与备份一致性**：新增数据库表或备份关键字段时，必须同步更新 `server/internal/backup/database.go`、`server/internal/backup/sections.go` 及迁移测试，确保导入/导出闭环完整。
4. **SDK 类型单一来源**：所有插件契约结构体与 manifest 新增结构必须标注 `ts` tag 并注册到 `server/internal/jsplugin/tsgen.go`，通过 `gentypes` 生成 `sdk/js/index.d.ts`，禁止手改生成段。
5. **只增不破的接口兼容**：旧 `kind` 清单、旧钩子别名与 `hostApi: 1` 插件必须持续兼容；未绑定新能力的请求路径只能引入一次常量级内存查询，不得增加明显延迟或内存分配。

### 5.2 新宿主能力准入评审 Checklist

在编写代码或评审 PR 前，必须逐项确认以下 6 个问题：

1. **三场景验证**：如果把当前目标插件的名字遮住，这套接口和数据结构能否直接用来实现另外两种不相关业务的插件？
2. **零业务硬编码**：新增的 Go 包、结构体字段、数据库列、错误码、配置项中，是否完全没有单一业务插件的专属词汇？
3. **热路径零额外开销**：未启用/未绑定该能力的普通 API Key 或站点，是否在入口和流式出口几乎零开销（最多一次 map 查找 / 指针判空）？
4. **安全与静默闭环**：高敏权限是否强制受信签名？静态互斥规则是否阻止了外发与持久化？日志、诊断抓包与错误信息是否完全不含原文？
5. **Fail-Closed 可恢复性**：插件熔断、停用或版本切换时，声明 `fail_closed` 的绑定是否仍能阻断流量而不是退化为放行？
6. **端到端离线可测**：插件作者能否在没有真实上游和外部服务的情况下，通过 `xlyra-plugin test` / `dry-run` 完整回归规则、钩子裁决与流式边界？

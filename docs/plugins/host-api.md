# 宿主 API（v1 / v2）

插件里可以直接使用下面这些全局对象，没有 `import`。除 `storage` 外，它们都是**纯计算**，不访问网络、文件或环境变量。类型声明见 `types/xlyra.d.ts`。

用到 `bytes`、`encoding`、扩展 `crypto`、`storage` 或 `ctx.settings` 的插件建议在清单中声明 `"hostApi": 2`（向下兼容 `"hostApi": 1`）。

参数类型不对、计算失败或未声明权限时，函数会**抛出普通的 JS 异常**，可以用 `try/catch` 接住；不接住则整个钩子调用失败，记为 `js_exception`。

## bytes（hostApi 2）

所有返回字节的函数均返回当前运行时原生的 `Uint8Array`。

| 函数 | 说明 |
| --- | --- |
| `bytes.fromUTF8(text)` | 字符串转 UTF-8 `Uint8Array` |
| `bytes.toUTF8(u8)` | `Uint8Array` 转字符串；非合法 UTF-8 时抛异常 |
| `bytes.fromHex(hex)` | 十六进制字符串转 `Uint8Array` |
| `bytes.toHex(u8)` | `Uint8Array` 转小写十六进制字符串 |
| `bytes.fromBase64(b64)` | 标准 base64（支持带或不带填充）转 `Uint8Array`（允许任意二进制字节） |
| `bytes.toBase64(u8)` | `Uint8Array` 转标准 base64 字符串 |
| `bytes.fromBase64URL(b64url)` | URL 安全 base64 转 `Uint8Array` |
| `bytes.toBase64URL(u8)` | `Uint8Array` 转无填充 URL 安全 base64 |
| `bytes.concat(chunks)` | 拼接 `Uint8Array[]`（上限 4 MiB） |
| `bytes.equal(a, b)` | 常量时间比较两个 `Uint8Array` 是否相等 |

## encoding（hostApi 2）

支持 `string | Uint8Array`（`BinaryLike`）输入的编解码工具：

| 函数 | 说明 |
| --- | --- |
| `encoding.hexEncode(data)` / `encoding.hexDecode(hex)` | 十六进制编码 / 解码为 `Uint8Array` |
| `encoding.base64Encode(data)` / `encoding.base64Decode(b64)` | 标准 base64 编码 / 解码为 `Uint8Array` |
| `encoding.base64URLEncode(data)` / `encoding.base64URLDecode(b64url)` | URL 安全 base64 编码 / 解码为 `Uint8Array` |
| `encoding.utf8Encode(text)` / `encoding.utf8Decode(u8)` | UTF-8 编码 / 解码 |

## codec（hostApi 1 兼容）

| 函数 | 说明 |
| --- | --- |
| `codec.base64Encode(s)` | 标准 base64 编码 |
| `codec.base64Decode(s)` | 标准 base64 解码，结果必须是合法 UTF-8，否则抛异常 |
| `codec.base64URLEncode(s)` | URL 安全 base64，无填充 |
| `codec.base64URLDecode(s)` | 对应的解码 |

## crypto

`data` 和 `key` 均支持传入 `string`（按 UTF-8）或 `Uint8Array`；`encoding` 可选 `"hex"`（默认）、`"base64"`、`"base64url"`。

| 函数 | 说明 |
| --- | --- |
| `crypto.md5(data, encoding?)` | MD5 摘要（默认返回 hex） |
| `crypto.sha1(data, encoding?)` | SHA-1 摘要（默认返回 hex） |
| `crypto.sha256(data, encoding?)` | SHA-256 摘要（默认返回 hex，兼容 v1 单参数调用） |
| `crypto.sha512(data, encoding?)` | SHA-512 摘要（默认返回 hex） |
| `crypto.digestBytes(alg, data)` | 返回 `Uint8Array` 摘要，`alg` 为 `"md5" \| "sha1" \| "sha256" \| "sha512"` |
| `crypto.hmac(alg, key, data, encoding?)` | HMAC 摘要（默认返回 hex） |
| `crypto.hmacBytes(alg, key, data)` | 返回 `Uint8Array` 的 HMAC |
| `crypto.hmacSHA256(key, message)` | v1 兼容：返回**标准 base64** 的 HMAC-SHA256 |
| `crypto.randomBytes(length)` | 生成加密安全的随机 `Uint8Array`（`0..65536` 字节） |
| `crypto.aesEncrypt(alg, key, iv, plaintext, aad?)` | AES 加密，返回 `Uint8Array`。支持 `aes-128-gcm`、`aes-256-gcm`（12 字节 nonce，密文末尾含 16 字节 tag，可选 `aad`）、`aes-128-cbc`、`aes-256-cbc`（16 字节 IV，自动 PKCS#7 填充） |
| `crypto.aesDecrypt(alg, key, iv, ciphertext, aad?)` | 对应的 AES 解密，返回 `Uint8Array` |
| `crypto.rsaEncrypt(publicKeyPEM, plaintext, padding?)` | 用 PEM 公钥（`PUBLIC KEY` 或 `RSA PUBLIC KEY`）加密，返回 `Uint8Array`。`padding` 支持 `"oaep-sha256"`（默认）、`"oaep-sha1"`、`"pkcs1v15"` |

## jwt

| 函数 | 说明 |
| --- | --- |
| `jwt.signHS256(payload, secret)` | 用 HS256 签发 JWT，`payload` 必须是对象 |
| `jwt.decodeHS256(token, secret)` | 校验签名并返回载荷；算法不是 HS256、签名不对、载荷不是对象时抛异常 |

## storage（需声明 `"permissions": ["storage"]`）

插件私有的同步 KV 存储，随调用上下文自动按**插件 × 作用域（`global` / `site` / `credential`）**隔离。未声明 `"storage"` 权限时调用会抛 `permission_denied`。

| 函数 | 说明 |
| --- | --- |
| `storage.get(key)` | 读取 JSON 值；不存在时返回 `null` |
| `storage.set(key, value)` | 写入可 JSON 序列化的值（不能为 `undefined`）；若内容包含当前秘密袋中的凭据明文，会抛 `secret_leak_rejected` 拒绝写入 |
| `storage.delete(key)` | 删除键，返回布尔值表示原先是否存在 |
| `storage.keys()` | 返回当前作用域下按字典序排列的键名数组 |

每个作用域最多存储 **64** 个键、合计 **64 KiB**。

## settings（`ctx.settings`）

当清单声明了 `settings` 数组时，每个钩子的 `ctx.settings` 会注入合并后的只读配置值（合并优先级：`站点级覆盖 > 插件全局配置 > 清单 default`）。头模板里的 `{{setting.<key>}}` 用同一份值。

## signRequest(ctx, req)

任何插件都可以导出 `signRequest`。挂到站点上之后，按「站点类型底座 → 挂载顺序」依次调用，后一个看到前一个改过的请求。protocol 插件自己的代签不进这条链。

`ctx` 在 `candidate`、`settings` 之外还有只读字段：`model`、`stream`、`protocol`、`now`（Unix 毫秒）、`sessionKey`（与 `{{session.key}}` 相同，取不到会话时为空串）、`modelMeta`（模型 meta）。只有清单声明了 `secret.use` 时才有 `credential`：API Key 站点是 `{ api_key }`，账号池是去掉 `cred.` 前缀后的字段表。这次调用里 `log.*` 会被丢弃，插件也不能发网络请求。

`req` 是 `{ method, url, headers, body }`。返回两种形状之一：

- 直接改写：`{ headers?, query?, body? }`，至少一项。`headers` 与 `query` 各最多 32 项，值不能含控制字符。`body` 替换整段正文（最大 16 MiB）；仅当改写前后仍为 JSON 对象时，JSON 里的 `model`、`stream` 必须与改写前相同（允许在 `signRequest` 中将正文编码为非 JSON 字节/文本流）。不需要 `secret.use`。
- 代签：`{ stringToSign, algorithm, header, encoding?, prefix?, headers? }`。必须声明 `secret.use`，由宿主用当前凭据做 HMAC。

钩子抛错、超时或返回不合法时，这次尝试记为 `upstream_sign_failed` 并中止（可换下一个凭据），不会带着缺头的请求发出去。

## transformStream(ctx, event)

当声明式 `stream` 配置不足以覆盖自定义流协议时，底座插件、协议插件或挂载的叠加插件可以导出 `transformStream(ctx, event)`（热池运行，单事件时限 **5 毫秒**），按「协议插件 / 站点底座 → 挂载顺序」逐个 SSE 事件链式改写：

- `ctx` 包含只读的 `siteType`、`model`、`protocol` 与合并后的 `settings`。
- `event` 为 `{ event?: string, data: string }`。
- 返回 `{ data?: string, event?: string, drop?: boolean, done?: boolean }`：`drop: true` 会丢弃当前事件不向下游写出；`done: true` 标记流在当前事件后结束。

## onSchedule(ctx, steps)

声明了 `"schedule"` 权限并导出 `onSchedule(ctx, steps)` 的插件，在管理员开启周期计划后由宿主按周期调用（单步时限 **1 秒**，最多 4 步请求）。

- **账号池站点底座插件**：宿主会按该站点类型下的每个已启用账号分别调用 `onSchedule(ctx, steps)`。`ctx.account` 提供非敏感字段与 `region`，请求头、路径或正文可使用 `{{secret.<field>}}` 占位符，由宿主在校验 `hosts`（或区域 `controlEndpoint`）后填入凭据并自动对返回结果脱敏，JS 全程看不到明文密钥；存储自动绑定到 `credential` 作用域。账号池分步请求（如 `listModels`、`onSchedule`）还可选指定 `endpoint: "gateway"`（改用该账号所属区域的 `endpoints.gateway[0]` 作为基址）和 `sign: true`（在 `{{secret.*}}` 替换后复用插件导出的 `signRequest` 对出站请求签名，签名产生的头不会回流到 `steps` 或日志）。
- **叠加插件 / 通用周期插件**：若挂载到了站点，宿主按每个挂载站点调用（`ctx.site` 含 `id`、`name`、`baseURL`、`siteType`，存储为 `site` 作用域）；未挂载站点时按全局调用一次（存储为 `global` 作用域）。返回 `{ request }` 时必须同时声明 `"http"` 权限与目标主机的 `"net:<host>"`（支持 `net:*.example.com`），相对路径基于 `ctx.site.baseURL`（或清单 `defaultBaseURL`）解析，也可直接写完整的 `http(s)://` URL。
- 完成后返回 `{ result: { status?: "ok" | "skipped", summary?: string } }` 或 `{ error: { code, message } }`，执行结果记入插件动作日志。

## utils 与 log

- `utils.uuid()`：随机 UUID v4。头模板 `{{uuid}}` 每次渲染也生成一个新的。
- `log.debug(msg, fields?)` / `log.info(...)` / `log.warn(...)`：每次调用最多保留 **50** 条，每条最多 **2048** 字节，超出会截断或丢弃。**不要把响应里的敏感内容写进日志。**

## 语言环境

- 引擎是 moejs，ES 模块，支持 ES2023 的常用特性，时区固定为 UTC。
- 可用的内置对象：`Date`、`Math`、`JSON`、`Uint8Array`、`TextEncoder`、`structuredClone`、`globalThis` 等标准对象。
- `eval`、`new Function` 和动态 `import()` 被禁用；每次调用在独立或复用的运行时会话中执行，跨调用状态请使用 `storage`，不要依赖模块级全局变量。

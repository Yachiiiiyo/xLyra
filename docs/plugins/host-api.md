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

当清单声明了 `settings` 数组时，每个钩子的 `ctx.settings` 会注入合并后的只读配置值（合并优先级：`站点级覆盖 > 插件全局配置 > 清单 default`）。

## utils 与 log

- `utils.uuid()`：随机 UUID v4。
- `log.debug(msg, fields?)` / `log.info(...)` / `log.warn(...)`：每次调用最多保留 **50** 条，每条最多 **2048** 字节，超出会截断或丢弃。**不要把响应里的敏感内容写进日志。**

## 语言环境

- 引擎是 moejs，ES 模块，支持 ES2023 的常用特性，时区固定为 UTC。
- 可用的内置对象：`Date`、`Math`、`JSON`、`Uint8Array`、`TextEncoder`、`structuredClone`、`globalThis` 等标准对象。
- `eval`、`new Function` 和动态 `import()` 被禁用；每次调用在独立或复用的运行时会话中执行，跨调用状态请使用 `storage`，不要依赖模块级全局变量。

# 原语与状态槽示例（`example.primitives`）

演示插件基座阶段 1 的核心原语：

- `bytes` / `encoding` / `crypto`：`Uint8Array` 编解码、HMAC-SHA256、AES-256-GCM 加解密；
- `storage`：声明 `"permissions": ["storage"]` 后在 `probe` 钩子中读写站点级私有 KV；
- `settings`：通过 `ctx.settings.clientVersion` 读取管理员配置（带默认值与正则约束）；
- 样本自检：覆盖 `probe`（带 `storage` 前后状态断言）与 `rewritePayload`（带 `settings` 注入断言）。

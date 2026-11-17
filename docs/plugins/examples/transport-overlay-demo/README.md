# 传输与多钩子叠加示例

阶段 4 示例：省略 `kind`，声明 `transport.profile`（`claude_code`）与通配网络权限 `net:*.example.com`，同时导出 `rewritePayload`、`check`、`classifyError` 供站点多插件叠加编排使用。

```bash
xlyra-plugin test docs/plugins/examples/transport-overlay-demo
```

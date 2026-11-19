# 无 kind 叠加示例

清单不写 `kind`，同时导出 `probe`、`classifyError`、`listModels`。`xlyra-plugin build` 会把这三个名字写入 `exports`。

```bash
xlyra-plugin test docs/plugins/examples/hooks-overlay-demo
```

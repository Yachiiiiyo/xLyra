# 请求叠加示例

清单不写 `kind`，也不声明站点类型。顶层 `request.headers` 用 `{{random.hex:40}}`、`{{session.key}}`、`{{setting.clientVersion}}`。`signRequest` 用直接改写模式追加一个头。

```bash
xlyra-plugin test docs/plugins/examples/request-overlay-demo
```

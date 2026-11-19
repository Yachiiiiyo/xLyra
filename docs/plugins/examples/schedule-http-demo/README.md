# 定时计划与通用代发示例

阶段 5 示例：省略 `kind`，声明 `schedule`、`http`、`net:api.example.com` 与 `storage` 权限，配置每 60 分钟触发 `onSchedule` 发起受限出站请求并记录状态。

```bash
xlyra-plugin test docs/plugins/examples/schedule-http-demo
```

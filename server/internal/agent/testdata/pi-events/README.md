# Pi 原始事件样本(pi-events)

Pi 1.4.0 在 mock LLM 下的真实 stdout 事件流,用于后端翻译器与前端时间线还原的回放测试。来源为 PoC(见 `docs/pi-agent-integration/poc/RESULTS-G.md`,编号对应该文档的用例)。

| 文件 | 场景 | 来源(results/ 下相对路径) | RESULTS-G 编号 |
| --- | --- | --- | --- |
| `basic.jsonl` | 基本对话(流式文本) | `T2/events-basic.jsonl` | G1-1 |
| `err-500.jsonl` | LLM 返回 HTTP 500 | `T2/events-err-500-default.jsonl` | G1-6a |
| `tool-echo.jsonl` | 单工具调用(echo) | `T3/events-echo.jsonl` | G1-2a |
| `tool-parallel.jsonl` | 并行工具调用 | `T3/events-parallel.jsonl` | G1-2b |
| `tool-unknown-bash.jsonl` | 未知工具(由 bash 兜底) | `T3/events-unknown-bash.jsonl` | G1-2c |
| `approval-allow.jsonl` | 权限审批:允许 | `T4/events-t4-allow.jsonl` | G1-3a |
| `approval-deny.jsonl` | 权限审批:拒绝 | `T4/events-t4-deny.jsonl` | G1-3b |
| `approval-timeout.jsonl` | 权限审批:超时 | `T4/events-t4-timeout.jsonl` | G1-3c |
| `approval-dual.jsonl` | 权限审批:并行双请求 | `T4/events-t4-dual.jsonl` | G1-3d |
| `approval-abort.jsonl` | 权限审批:等待中主动中止 | `T4/events-t4-abort.jsonl` | G1-3e |
| `compact-manual.jsonl` | 手动上下文压缩 | `C1-5/events.jsonl` | G1-4a |
| `compact-overflow.jsonl` | 上下文溢出自动压缩 | `C1-6/events-c1overflow.jsonl` | G1-4b |
| `abort-in-tool.jsonl` | 工具执行中中止 | `C1-4/events-tool.jsonl` | G1-5a |
| `abort-in-stream.jsonl` | 流式回复中中止 | `C3/events-stream.jsonl` | G1-5b |

## 注意:审批决定不在 stdout 里

**用户的审批决定不在 Pi stdout 事件流中**。`approval-*` 系列样本只含审批请求(`escalation_start` 等);测试按文件名约定补 `permission_result`:

- `approval-allow` → `allowed`
- `approval-deny` / `approval-timeout` → `denied`
- `approval-dual` → 第一个拒绝、第二个允许
- `approval-abort` → 无决定(等待中被中止)

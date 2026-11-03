# 前端 Agent 事件夹具(__fixtures__)

- **`translated-*.jsonl`**:PoC 草稿翻译器从 Pi 1.4.0 原始事件(见 `server/internal/agent/testdata/pi-events/`)翻译出的 xLyra 事件流。**正式翻译器实现后需用其重新生成**。审批类的 `permission_result` 是 PoC 按文件名约定补的,不是 Pi stdout 原生事件。
- **`timeline-*.json`**:上述事件流用 `reduceAgentEvent`(见 `../agent-events.ts`)回放后得到的时间线快照,用于前后对比与回归测试。

## 已知前端缺口(修复前快照反映的是现状)

1. `AgentRun` 缺 `errorMessage` 字段:error/cancelled 时错误文案无处展示(样本 `err-500`,HTTP 500 的报错信息在时间线里丢失)。
2. cancelled/error 收束时未决权限未落定:取消后权限卡片的 `decision` 一直是 null,仍可点击(样本 `t4-abort`)。
3. `ensureRun` 只在权限全部已决时才恢复 cancelled run:否则应新开 run、旧 run 保持 cancelled(样本 `t4-abort` 的第二轮「继续」)。

## 来源与对账

来自 `docs/pi-agent-integration/poc/results/G1/`,样本与场景编号的对应关系见 `docs/pi-agent-integration/poc/RESULTS-G.md`。

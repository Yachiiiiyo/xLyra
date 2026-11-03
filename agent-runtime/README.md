# agent-runtime

xLyra 内置 Agent 运行时：托管 [pi coding agent](https://www.npmjs.com/package/@earendil-works/pi-coding-agent)。后端(Go)以子进程方式拉起 `pi --mode rpc`,通过 stdin/stdout 的 JSON-RPC 与其通信。

- **锁定版本**:`@earendil-works/pi-coding-agent@1.4.0`(见 `package.json` / `package-lock.json`)。
- **环境要求**:Node ≥ 22.19。

## 安装

```sh
npm ci --ignore-scripts --prefix agent-runtime
```

## 运行时环境变量

Go 侧拉起子进程时统一注入,禁止运行时联网与上报:

```
PI_OFFLINE=1 PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0
```

## 升级流程

1. 改 `package.json` 里的版本号;
2. `npm install --package-lock-only --ignore-scripts --prefix agent-runtime` 刷新 lockfile;
3. 跑夹具回放/契约测试(`server/internal/agent/testdata/pi-events/` 与 `web/src/features/agent/lib/__fixtures__/`,样本过期需用新版 Pi 重新录制);
4. 手动测试一轮内置 Agent(基本对话、工具调用、权限审批、中断、上下文压缩)。

## 升级时必须核对的契约

- **会话文件格式**：`server/internal/agent/session` 按 Pi 1.4.0 的 v3 JSONL（`{"type":"session","version":3}` + 树条目）解析 transcript。Pi 后续版本若换格式（0.87.1 的包里已有未发布的 v4），transcript 会变空且不报错，升级前必须用新版本产出样本重测。
- **RPC 事件**：`server/internal/agent/translate/testdata` 的夹具与 golden 需用新版本重新生成并人工审阅差异。

## xLyra 扩展

`extension/xlyra.ts` 是随镜像发布的小型 Pi 扩展,由 Go 以 `pi -e <扩展路径>` 加载,承担三件事:

1. **路径约束**(第二层防线):`read/ls/grep/find` 只能访问会话工作目录(Pi 进程 cwd)之内、或 `readRoots`(启用技能的目录,Go 侧随 `xlyra.json` 传入)之内的路径;`write/edit` 仍只允许工作目录之内,即使目标位于 `readRoots` 内也拒绝。绝对路径、`~` 前缀、`..` 逃逸、指向允许范围外的符号链接(含悬空链接)一律拒绝;`full` 模式也不放宽。路径约束对 bash 不适用(沙箱边界由执行器的 Landlock 负责)。
2. **审批**:`write`/`edit`/`bash` 执行前经 `extension_ui_request(confirm)` 向用户确认;拒绝/取消/超时都不执行。bash 在 `ask` 模式下逐条命令审批,`full` 模式仅高危命令审批(见下节)。
3. **沙箱 bash**:配置启用时覆盖内置 `bash`,命令一律经主进程的执行器在专用 uid + Landlock + seccomp + rlimit 下执行,失败即关闭、绝不回退本地(见下节)。

配置文件为 `${PI_CODING_AGENT_DIR}/xlyra.json`:

```json
{
	"permissionMode": "ask",
	"readRoots": ["/data/agent/skills/pdf-tools"],
	"bash": { "enabled": true, "socket": "/run/xlyra/agent/exec.sock", "defaultTimeout": 120, "maxTimeout": 600 }
}
```

- `ask`:写类工具与 bash 命令须用户确认(默认);
- `full`:跳过 write/edit 审批,bash 仅命中高危模式时确认;路径约束照常生效;
- `readRoots`:只读根(启用技能的目录绝对路径),只放宽 `read/ls/grep/find`;每项须是存在的绝对路径且深度 ≥ 2 段,非法项忽略,缺省为 `[]`;
- `bash`:沙箱执行器配置,见下节。

读不到、解析失败、值非法一律按 `ask` 处理(fail-closed);Go 每次 run 重写该文件并拉起新进程,扩展只在启动时读一次。除启用沙箱 bash 时用同名工具覆盖内置 bash 外不注册新工具、不改系统提示,工具白名单仍由 Go 的 `--tools` 控制。

### bash 与沙箱执行器协议

`xlyra.json` 的 `bash` 字段启用后,扩展用 `createBashTool(cwd, { operations })` 以同名工具覆盖内置 bash:工具名、描述、参数 schema、输出截断全部沿用 Pi 内置实现,只把执行方式换成 xLyra 主进程的 Unix 套接字执行器(专用 uid + Landlock + seccomp + rlimit 下执行);用户的 `!` 前缀命令与 RPC 的 `bash` 命令(`user_bash` 钩子)同样只走执行器。

配置字段(`"bash": { "enabled": true, "socket": "/abs/path/exec.sock", "defaultTimeout": 120, "maxTimeout": 600 }`):

- `enabled` 必须严格为 `true`;`socket` 必须是绝对路径字符串;`defaultTimeout`/`maxTimeout` 必须是正整数,缺省 120/600,`defaultTimeout > maxTimeout` 时取 `maxTimeout`;
- 读不到/非法/缺 `enabled` 一律按**未启用**处理(fail-closed):不覆盖 bash、不挂 `user_bash` 钩子、不做任何审批。契约:**Go 侧在沙箱自检不通过或未配置时不会把 `bash` 放进 `--tools`**,此时模型侧根本看不到 bash 工具(扩展也不会拦截它,内置行为保持原样)。

请求/响应格式(一行 JSON,UTF-8):请求 `{"cmd":<命令>,"cwd":<工作目录>,"timeout":<秒>}\n`,**不传 env**,环境由执行器构造;`timeout` 永远是正整数(Pi 没传用 `defaultTimeout`,传了与 `maxTimeout` 取小)。响应为 JSON 行流:`{"t":"out"|"err","d":"<base64>"}` 逐块输出(out/err 都交给 Pi);`{"t":"exit","code":N,"signal":"","timed_out":bool,"reaped":N,"error":""}` 结束:

- `error` 非空 → 工具报错 `sandbox: <error>`;`timed_out` 为真 → 报错 `timeout:<秒>`(Pi 据此显示统一的"命令超时"提示);否则 `exitCode = code`,非零时 Pi 按失败结果处理;
- **abort**:用户取消/run 中止时扩展立即销毁连接(执行器据此杀整棵进程树并 reap),命令以"已中止"收场;连接前已中止同样处理;
- **超时兜底**:本地定时器在 `timeout + 3` 秒后仍无 `exit` 就销毁连接并报超时(执行器自身也会在 `timeout` 杀进程,这是保险网);
- **失败即关闭**:连接错误(`ECONNREFUSED`/`ENOENT`…)→ 报错 `sandbox unavailable: <原因>`;响应不是合法 JSON 行、`exit` 之前连接关闭、单行超过 4 MiB,都是协议错误;**任何路径都不会回退到本地执行**,扩展源码里也没有本地执行 API。

审批(与 write/edit 同一套 `extension_ui_request(confirm)`,必须传 `signal`,异常/abort 一律按拒绝):

- `permissionMode=ask`:每条命令都审批,title `允许执行命令?`,message 为 JSON `{toolCallId, toolName:"bash", command:[<完整命令字符串>], note:"工作目录: <相对 cwd 的路径或 .>"}`——`command` 是单元素字符串数组(对齐 Go 侧 `ParseApprovalMessage` 的 `Command []string`),命令原文完整展示不截断,超过 8 KiB 直接拒绝("命令过长");
- `permissionMode=full`:仅命中高危模式时用 title `这条命令有风险,允许执行?` 审批,message 同上,`note` 追加 `;命中规则: <规则名>`。高危规则(rm 递归删除高危目标、mkfs、dd 写块设备、重定向写磁盘设备、fork 炸弹、下载脚本直接执行、递归改根目录权限、关机/重启、git 强推、sudo、覆盖 SSH/shell 配置)是**尽力而为的提示层,不是安全边界**,真正的边界是执行器的 Landlock/seccomp/rlimit;
- 拒绝时工具返回 `用户拒绝了本次命令`;路径约束对 bash 不适用(沙箱的 Landlock 才是边界),`cwd` 原样使用 Pi 给的工作目录。

### 运行测试

```sh
npm test --prefix agent-runtime
```

测试用本机 mock LLM 拉起真实的 Pi 1.4.0 进程(不需要网络与密钥),并用 Node 假执行器(`test/lib/fake-exec.mjs`,Unix 套接字上的可编程协议应答,不执行真实命令)覆盖审批允许/拒绝/中止、路径约束、配置 fail-closed、工具白名单、沙箱 bash 协议(审批/拒绝/abort/超时/失败即关闭/协议错误/高危规则)等场景。

**扩展与 Pi 版本绑定**:扩展依赖 Pi 1.4.0 的内置工具参数与扩展事件契约(`tool_call`、`extension_ui_request` 等)。升级 Pi 版本前必须先跑上述测试,全绿后再合入。

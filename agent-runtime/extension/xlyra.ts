// xLyra Pi 扩展:路径约束(第二层防线)+ write/edit 审批 + bash(沙箱 / 本机两种模式)。
//
// - 路径约束:read/ls/grep/find 只能访问会话工作目录(Pi 进程 cwd)之内、或 readRoots
//   (启用技能的目录)之内的路径;write/edit 仍只允许工作目录之内,即使目标位于
//   readRoots 内也拒绝。绝对路径、~ 前缀、.. 逃逸、指向允许范围外的符号链接(含
//   悬空链接)一律拒绝。路径约束对 bash 不适用:沙箱执行器的 Landlock 才是 bash 的
//   边界,这里不做词法检查。
// - 审批:permissionMode=ask 时 write/edit/bash 执行前必须经用户确认(bash 逐条命令
//   审批,卡片展示完整命令);auto(「替我审批」)时 write/edit 直接放行,bash 逐条
//   发审批并由 Go 分类器自动决定(命中高危规则时备注带规则名,供 runner 确定性
//   拒绝);full(「完全访问」)时 write/edit/bash 一律不审批,高危规则不参与。
//   路径约束照常生效,工具白名单由 Go 侧 --tools 控制。
// - 沙箱 bash:运行配置 runtime.bash 启用时,用 createBashTool 以同名工具覆盖内置
//   bash(描述、参数 schema、输出截断全部不变),模型与用户的 bash 命令(! 前缀与
//   RPC 的 bash)一律发给 xLyra 主进程的 Unix 套接字执行器(专用 uid + Landlock +
//   seccomp + rlimit 下执行,环境也由执行器构造)。失败即关闭:连不上、协议错误
//   一律报错,绝不回退到本地执行;未启用时不覆盖、不拦截,内置行为保持原样
//   (契约:Go 侧此时不把 bash 放进 --tools)。
// - 本机 bash:runtime.bash.mode 为 "local"(没有 socket)时,命令不经执行器,直接用 Pi 自带的
//   本地 shell 执行(与 xLyra 同权限、可联网),只在外面套 defaultTimeout/maxTimeout 超时;审批
//   与高危规则同沙箱模式完全一致。用户 ! 前缀与 RPC 的 bash 同样走这套本机 operations。
// - fail-closed:配置读不到/非法一律按 ask、readRoots 为空、bash 与生图未启用;钩子内任何
//   意外错误都阻断工具调用。
// - 运行配置:Go 经环境变量 XLYRA_RUN_CONFIG 传入单个 JSON:runtime(权限/只读根/
//   bash/生图)、provider(原样注册为 xlyra provider)、agentsMd(经 context files 注入)。
//
// 沙箱 bash 不新增其他命令工具;生图配置有效时额外注册 image_generation,不触发审批。
// 快速任务配置(runtime.fastTask)有效时额外注册 summarize_file:把文件前 256 KiB 经本 run
// 令牌交给后端的快速模型提炼摘要,结果只回文本;同样不触发审批,但走路径约束(工作目录或
// readRoots 内,execute 内再做一次 realpath 防御校验),不修改任何文件。
// question 工具无条件注册:通过 editor 对话向用户提多选题,结果回给模型;不触发审批、
// 不受路径约束,每个进程最多 5 次调用,取消/异常一律返回「用户取消了提问」。
// reason 里只回显模型的原始输入,不回显 realpath 之后的宿主机真实路径。
import { closeSync, fstatSync, lstatSync, openSync, readFileSync, readSync, realpathSync, readlinkSync } from "node:fs";
import { mkdir, rename, rm, writeFile } from "node:fs/promises";
import net from "node:net";
import path from "node:path";
import { Type } from "@earendil-works/pi-ai";
import {
	createBashTool,
	createLocalBashOperations,
	type AgentToolResult,
	type BashOperations,
	type BashToolCallEvent,
	type ExtensionAPI,
	type ExtensionContext,
	type ExtensionToolContext,
	type ProviderConfig,
	type ToolCallEventResult,
} from "@earendil-works/pi-coding-agent";

type PermissionMode = "ask" | "auto" | "full";

const PATH_TOOLS = new Set(["read", "ls", "grep", "find", "write", "edit", "summarize_file"]);
const APPROVAL_TOOLS = new Set(["write", "edit"]);
// read/ls/grep/find/summarize_file 只读类工具:工作目录之外还可访问 readRoots(技能目录)
const READONLY_PATH_TOOLS = new Set(["read", "ls", "grep", "find", "summarize_file"]);
// ls/grep/find 未显式给 path 时作用于 cwd,天然在工作目录内
const OPTIONAL_PATH_TOOLS = new Set(["ls", "grep", "find"]);
const MAX_LINK_DEPTH = 64;
const BLOCK_REASON_GENERIC = "扩展检查失败,已拒绝";

// ---- 沙箱 bash:配置、协议与执行 ----
// 执行器请求是一行 JSON:{"cmd":命令,"cwd":工作目录,"timeout":秒}\n;不传 env,
// 环境由执行器构造。响应也是 JSON 行:{"t":"out"|"err","d":"<base64>"} 流式输出;
// {"t":"exit","code":N,"signal":"","timed_out":bool,"reaped":N,"error":""} 结束。
type BashSandboxConfig = { mode: "sandbox"; socket: string; defaultTimeout: number; maxTimeout: number };
type BashLocalConfig = { mode: "local"; defaultTimeout: number; maxTimeout: number };
type BashConfig = BashSandboxConfig | BashLocalConfig;
type ExecutorMessage =
	| { t: "out" | "err"; d: string }
	| { t: "exit"; code: number; signal?: string; timed_out?: boolean; reaped?: number; error?: string };

const DEFAULT_BASH_TIMEOUT_SECONDS = 120;
const MAX_BASH_TIMEOUT_SECONDS = 600;
// 本地兜底定时器比请求超时多给的宽限(执行器自己也会在 timeout 杀进程)
const TIMEOUT_GRACE_SECONDS = 3;
// 单行响应超过 4 MiB 视为协议错误
const MAX_RESPONSE_LINE_BYTES = 4 * 1024 * 1024;
// 审批卡片必须展示完整命令,但超过 8 KiB 的命令直接拒绝
const MAX_APPROVAL_COMMAND_BYTES = 8 * 1024;

// 启动时读一次即可:Go 每次 run 都会重建 XLYRA_RUN_CONFIG 并拉起新的 Pi 进程。
// 变量缺失或解析失败返回 null;各字段的合法性由各自的 load* 函数校验。
type RunConfig = { runtime?: unknown; provider?: unknown; agentsMd?: unknown };
let runConfigLoaded = false;
let runConfig: RunConfig | null = null;

function loadRunConfig(): RunConfig | null {
	if (runConfigLoaded) return runConfig;
	runConfigLoaded = true;
	try {
		const raw = process.env.XLYRA_RUN_CONFIG;
		if (!raw) return null;
		const parsed = JSON.parse(raw);
		if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) runConfig = parsed as RunConfig;
	} catch {
		runConfig = null;
	}
	return runConfig;
}

// 原 xlyra.json 的内容;不是对象时返回 null(各配置按未启用/ask 处理)。
function loadRuntimeConfig(): Record<string, unknown> | null {
	const runtime = loadRunConfig()?.runtime;
	if (!runtime || typeof runtime !== "object" || Array.isArray(runtime)) return null;
	return runtime as Record<string, unknown>;
}

// 读不到、解析失败、字段非法 → 一律按未启用(fail-closed:宁可工具不可用,也不本地执行)。
// enabled 必须严格为 true,socket 必须是绝对路径,超时必须是正整数(缺省 120/600)。
function loadBashConfig(): BashConfig | null {
	try {
		const parsed = loadRuntimeConfig();
		if (!parsed) return null;
		const bash = parsed.bash;
		if (!bash || typeof bash !== "object") return null;
		const record = bash as Record<string, unknown>;
		if (record.enabled !== true) return null;
		const mode = record.mode === undefined ? "sandbox" : record.mode;
		if (mode !== "sandbox" && mode !== "local") return null;
		const defaultTimeout = optionalPositiveInt(record.defaultTimeout, DEFAULT_BASH_TIMEOUT_SECONDS);
		const maxTimeout = optionalPositiveInt(record.maxTimeout, MAX_BASH_TIMEOUT_SECONDS);
		if (defaultTimeout === null || maxTimeout === null) return null;
		const clamped = Math.min(defaultTimeout, maxTimeout);
		if (mode === "local") return { mode: "local", defaultTimeout: clamped, maxTimeout };
		if (typeof record.socket !== "string" || !path.isAbsolute(record.socket)) return null;
		return { mode: "sandbox", socket: record.socket, defaultTimeout: clamped, maxTimeout };
	} catch {
		return null;
	}
}

// 正整数校验:字段缺省用 fallback;给了但不是正整数返回 null(整个配置按未启用处理)
function optionalPositiveInt(value: unknown, fallback: number): number | null {
	if (value === undefined) return fallback;
	if (typeof value !== "number" || !Number.isInteger(value) || value <= 0) return null;
	return value;
}

// 高危命令规则:尽力而为的提示层,不是安全边界——真正的边界是沙箱执行器的
// Landlock/seccomp/rlimit。命中只是在 auto 模式下发审批时把规则名带给 runner 作确定性
// 拒绝,full 模式不参与;漏报不构成漏洞,误报可接受。按「命令文本中出现」匹配,
// 不解析 shell 语义。
const HIGH_RISK_RULES: { name: string; pattern: RegExp }[] = [
	// rm 带递归标志(-r/-R/-rf/--recursive 等)且目标含绝对路径 /、~、$HOME、..、
	// 通配 * 或单独的点(整个工作目录)
	{
		name: "rm 递归删除高危目标",
		pattern: /(?:^|[\s;&|(])rm\s+(?:-[a-zA-Z]*r[a-zA-Z]*|--recursive)\b[^;&|]*(?:\/|~|\$HOME|\.\.|\*|(?<=\s)\.(?=[\s;&|)]|$))/,
	},
	// mkfs 系列
	{ name: "mkfs 格式化文件系统", pattern: /(?:^|[\s;&|(])mkfs[\w.-]*(?=[\s;&|)]|$)/ },
	// dd 写块设备
	{ name: "dd 直写磁盘设备", pattern: /(?:^|[\s;&|(])dd\b[^;&|]*\bof=\/dev\// },
	// 重定向写入磁盘设备(覆盖与追加同等危险)
	{ name: "重定向写入磁盘设备", pattern: /(?:^|[^>])\s*>{1,2}\s*\/dev\/(?:sd|nvme)/ },
	// fork 炸弹
	{ name: "fork 炸弹", pattern: /:\(\)\{/ },
	// curl/wget 下载直接进解释器执行;解释器带内联脚本参数(node -e/-p/--eval/--print、
	// python -c、sh/bash/zsh -c)时管道内容只是数据,不算命中
	{
		name: "下载脚本直接执行",
		pattern:
			/(?:^|[\s;&|(])(?:curl|wget)\b[^|;&]*\|\s*(?:sudo\s+)?(?:(?:ba|z)?sh\b(?![^|;&]*\s-[a-zA-Z]*c\b)|python[\w.-]*\b(?![^|;&]*\s-[a-zA-Z]*c\b)|node\b(?![^|;&]*\s(?:-[a-zA-Z]*[ep][a-zA-Z]*|--(?:eval|print))\b))/,
	},
	// 递归 chmod/chown 作用于根目录
	{
		name: "递归修改根目录权限",
		pattern: /(?:^|[\s;&|(])(?:chmod|chown)\b[^;&|]*\s(?:-[a-zA-Z]*R[a-zA-Z]*|--recursive)\b[^;&|]*\s\/(?![^\s;&|)])/,
	},
	// 关机/重启
	{ name: "关机或重启", pattern: /(?:^|[\s;&|(])(?:shutdown|reboot|halt|poweroff)\b/ },
	// git 强制推送
	{
		name: "git 强制推送",
		pattern: /(?:^|[\s;&|(])git\s+push(?:\s+[^;&|]*)?\s+(?:--force(?:-with-lease)?|-[a-zA-Z]*f[a-zA-Z]*)\b/,
	},
	// sudo 提权
	{ name: "sudo 提权", pattern: /(?:^|[\s;&|(])sudo\b/ },
	// 重定向覆盖 SSH/shell 配置
	{ name: "覆盖 SSH 或 shell 配置文件", pattern: /(?:^|[^>])\s*>\s*(?:~\/\.ssh|\.bashrc)/ },
];

// 返回命中的规则名列表;未命中返回 null。纯函数,便于测试。
function matchHighRiskRules(command: string): string[] | null {
	const names = HIGH_RISK_RULES.filter((rule) => rule.pattern.test(command)).map((rule) => rule.name);
	return names.length > 0 ? names : null;
}

// 本机 operations:复用 Pi 自带的本地 shell 执行(含进程树终止与 abort),只统一超时:
// Pi 没传用 defaultTimeout,传了与 maxTimeout 取小。
function localOperations(config: BashLocalConfig): BashOperations {
	const base = createLocalBashOperations();
	return {
		exec: (command, cwd, options) => {
			const requested = typeof options.timeout === "number" && options.timeout > 0 ? options.timeout : config.defaultTimeout;
			const seconds = Math.max(1, Math.min(Math.floor(requested), config.maxTimeout));
			return base.exec(command, cwd, { ...options, timeout: seconds });
		},
	};
}

// 沙箱执行器 operations:Pi 的 bash 工具与 user_bash(! 前缀/RPC bash)都走这里。
// 任何失败(连接错误、协议错误、超时、abort)都 reject,绝不本地执行。
function sandboxOperations(config: BashSandboxConfig): BashOperations {
	return {
		exec: (command, cwd, { onData, signal, timeout }) =>
			new Promise<{ exitCode: number | null }>((resolve, reject) => {
				// 发给执行器的超时永远是正整数:Pi 没传用 defaultTimeout,传了与 maxTimeout 取小
				const requested = typeof timeout === "number" && timeout > 0 ? timeout : config.defaultTimeout;
				const seconds = Math.max(1, Math.min(Math.floor(requested), config.maxTimeout));

				if (signal?.aborted) {
					reject(new Error("aborted")); // 连接前就已中止
					return;
				}

				const conn = net.createConnection(config.socket);
				let buffer = "";
				let finished = false;
				let hardTimer: NodeJS.Timeout | undefined;

				const finish = (settle: () => void) => {
					if (finished) return;
					finished = true;
					if (hardTimer !== undefined) clearTimeout(hardTimer);
					signal?.removeEventListener("abort", onAbort);
					conn.destroy(); // 断开连接:执行器据此杀整棵进程树并 reap
					settle();
				};
				const onAbort = () => finish(() => reject(new Error("aborted")));
				signal?.addEventListener("abort", onAbort, { once: true });

				// 本地兜底:超时 + 3 秒仍未收到 exit 就不再等执行器
				hardTimer = setTimeout(
					() => finish(() => reject(new Error(`timeout:${seconds}`))),
					(seconds + TIMEOUT_GRACE_SECONDS) * 1000,
				);

				conn.on("connect", () => {
					conn.write(JSON.stringify({ cmd: command, cwd, timeout: seconds }) + "\n");
				});

				conn.on("data", (chunk) => {
					buffer += chunk.toString("utf8");
					let newlineAt: number;
					while ((newlineAt = buffer.indexOf("\n")) >= 0) {
						const line = buffer.slice(0, newlineAt);
						buffer = buffer.slice(newlineAt + 1);
						if (line.length === 0) continue;
						if (Buffer.byteLength(line, "utf8") > MAX_RESPONSE_LINE_BYTES) {
							finish(() => reject(new Error("sandbox: response line too large")));
							return;
						}
						let message: ExecutorMessage;
						try {
							message = JSON.parse(line) as ExecutorMessage;
						} catch {
							finish(() => reject(new Error("sandbox: malformed response")));
							return;
						}
						if (message.t === "out" || message.t === "err") {
							if (typeof message.d !== "string") {
								finish(() => reject(new Error("sandbox: malformed response")));
								return;
							}
							onData(Buffer.from(message.d, "base64")); // out/err 都交给 Pi
						} else if (message.t === "exit") {
							if (typeof message.code !== "number") {
								finish(() => reject(new Error("sandbox: malformed response")));
								return;
							}
							if (typeof message.error === "string" && message.error !== "") {
								finish(() => reject(new Error(`sandbox: ${message.error}`)));
								return;
							}
							if (message.timed_out === true) {
								// Pi 识别 timeout:<秒> 前缀,会给出统一的超时提示
								finish(() => reject(new Error(`timeout:${seconds}`)));
								return;
							}
							finish(() => resolve({ exitCode: message.code }));
						} else {
							finish(() => reject(new Error("sandbox: malformed response")));
							return;
						}
					}
					// 无换行的半行超过上限:同样按协议错误销毁连接
					if (Buffer.byteLength(buffer, "utf8") > MAX_RESPONSE_LINE_BYTES) {
						finish(() => reject(new Error("sandbox: response line too large")));
					}
				});

				// 连不上(ECONNREFUSED/ENOENT…)→ 报不可用,绝不回退本地
				conn.on("error", (error) =>
					finish(() => reject(new Error(`sandbox unavailable: ${error.message}`))),
				);
				// exit 之前连接关闭:协议错误,已收到的输出不能当成成功
				conn.on("close", () =>
					finish(() => reject(new Error("sandbox: connection closed before exit"))),
				);
			}),
	};
}

// bash 审批:full(完全访问)直接放行,只保留空/超长的输入校验;ask/auto 逐条审批,
// auto 命中高危规则时在备注里注明规则名,由 Go 分类器决定。异常/abort 一律按拒绝;
// message.command 对齐 Go 侧 ParseApprovalMessage 的 []string,完整命令不截断。
async function approveBashCall(
	event: BashToolCallEvent,
	ctx: ExtensionContext,
	permissionMode: PermissionMode,
): Promise<ToolCallEventResult | undefined> {
	const command = event.input?.command;
	if (typeof command !== "string" || command === "") {
		return { block: true, reason: BLOCK_REASON_GENERIC };
	}
	if (Buffer.byteLength(command, "utf8") > MAX_APPROVAL_COMMAND_BYTES) {
		return { block: true, reason: "命令过长,已拒绝(超过 8 KiB)" };
	}
	if (permissionMode === "full") return; // 完全访问:输入校验通过即放行,不审批
	const hits = permissionMode === "auto" ? matchHighRiskRules(command) : null;

	const cwd = ctx.cwd || process.cwd();
	const workdir = path.relative(process.cwd(), cwd) || ".";
	const payload: Record<string, unknown> = {
		toolCallId: event.toolCallId,
		toolName: "bash",
		command: [command],
		note:
			hits === null
				? `工作目录: ${workdir}`
				: `工作目录: ${workdir};命中规则: ${hits.join("、")}`,
	};
	let confirmed = false;
	try {
		// 必须传 signal,否则等待审批时 abort 会被卡住
		confirmed = await ctx.ui.confirm("允许执行命令?", JSON.stringify(payload), {
			signal: ctx.signal,
		});
	} catch {
		confirmed = false; // abort 等异常一律按拒绝处理
	}
	if (!confirmed) return { block: true, reason: "用户拒绝了本次命令" };
	return; // 放行
}

// 读不到、解析失败、值非法 → 一律按 ask(fail-closed)。
function loadPermissionMode(): PermissionMode {
	const mode = loadRuntimeConfig()?.permissionMode;
	return mode === "auto" || mode === "full" ? mode : "ask";
}

// 读取并校验只读根(readRoots,启用技能的目录):只放开 read/ls/grep/find,
// write/edit 仍限工作目录。fail-closed:配置读不到/字段非法 → 空数组;数组里的
// 非法项逐个忽略,不影响其余项。
function loadReadRoots(): string[] {
	try {
		const parsed = loadRuntimeConfig();
		if (!parsed || !Array.isArray(parsed.readRoots)) return [];
		const roots: string[] = [];
		for (const item of parsed.readRoots) {
			const root = validReadRoot(item);
			if (root !== null) roots.push(root);
		}
		return roots;
	} catch {
		return [];
	}
}

// 单个 readRoot 的校验,返回 realpath 后的绝对路径;不合法返回 null:
// - 必须是字符串绝对路径;
// - realpath 必须存在(技能目录由 Go 侧保证存在);
// - 原始输入与 realpath 的深度都 ≥ 2 段(原始路径也查,防止 /etc 这类经符号
//   链接到深层路径的浅路径绕过;过宽根会放开整个文件系统,fail-closed)。
function validReadRoot(item: unknown): string | null {
	if (typeof item !== "string" || !path.isAbsolute(item)) return null;
	if (pathDepth(item) < 2) return null;
	let real: string;
	try {
		real = realpathSync(item);
	} catch {
		return null; // 不存在的目录直接忽略
	}
	if (pathDepth(real) < 2) return null;
	return real;
}

// 绝对路径根之下的段数:/ 为 0,/tmp 为 1,/tmp/x 为 2
function pathDepth(absPath: string): number {
	const { root } = path.parse(absPath);
	return absPath.slice(root.length).split(path.sep).filter((s) => s.length > 0).length;
}

function isErrnoException(error: unknown): error is NodeJS.ErrnoException {
	return typeof error === "object" && error !== null && "code" in error;
}

// 把模型的路径输入换算成绝对路径;返回 null 表示必然逃逸(~、file://),直接拒绝。
// Pi 的 resolveToCwd 会剥掉开头的 "@",这里必须保持一致,否则 "@/x" 会被误判成相对路径。
function toAbsolute(raw: string, cwd: string): string | null {
	let p = raw;
	if (p.startsWith("@")) p = p.slice(1);
	if (p.startsWith("~")) return null; // ~ 展开为 home,必在会话目录之外
	if (p.startsWith("file://")) return null; // Pi 会转成本地绝对路径,统一拒绝(fail-closed)
	return path.isAbsolute(p) ? path.resolve(p) : path.resolve(cwd, p);
}

// 逐段解析符号链接(含嵌套与悬空链接),返回真实绝对路径。
// 等价于「realpathSync 最长已存在祖先 + 拼回不存在的尾部」,但对悬空符号链接也能
// 得出真实落点;否则 write 一个经由悬空链接的路径会以词法比较漏过约束。
// 解析不了的段(尚不存在)只可能是待创建的普通名字,原样接回。
function resolveReal(absPath: string, depth = 0): string {
	if (depth > MAX_LINK_DEPTH) throw new Error("symlink depth exceeded");
	const { root } = path.parse(absPath);
	const segments = absPath.slice(root.length).split(path.sep).filter((s) => s.length > 0);
	let resolved = root;
	for (let i = 0; i < segments.length; i++) {
		const candidate = path.join(resolved, segments[i]);
		let stats;
		try {
			stats = lstatSync(candidate);
		} catch (error) {
			if (isErrnoException(error) && error.code === "ENOENT") {
				return path.join(resolved, ...segments.slice(i));
			}
			throw error;
		}
		if (stats.isSymbolicLink()) {
			const target = readlinkSync(candidate);
			// 相对链接目标基于链接所在目录解析;结果可能仍是链接,递归处理
			resolved = resolveReal(path.isAbsolute(target) ? target : path.resolve(resolved, target), depth + 1);
		} else {
			resolved = candidate;
		}
	}
	return resolved;
}

function isInside(parent: string, child: string): boolean {
	return child === parent || child.startsWith(parent.endsWith(path.sep) ? parent : parent + path.sep);
}

// 审批 message 里的简短说明,取不到就省略
function describeChange(toolName: string, input: Record<string, unknown>): string | undefined {
	if (toolName === "write" && typeof input.content === "string") {
		return `写入 ${Buffer.byteLength(input.content, "utf8")} 字节`;
	}
	if (toolName === "edit" && Array.isArray(input.edits)) {
		return `替换 ${input.edits.length} 处`;
	}
	return undefined;
}

// reason 里只用模型给的原始输入,避免泄露 realpath 之后的宿主机真实路径
function displayPath(raw: string): string {
	return raw.length > 200 ? `${raw.slice(0, 200)}...` : raw;
}

// ---- 生图工具:经本 run 令牌调用网关,图片只落工作区、不回灌模型 ----
type ImageGenerationConfig = { endpoint: string; tokenFile: string; maxBytes: number; timeoutSeconds: number };
type ImageGenerationInput = { prompt: string; size?: string; quality?: string };

function loadImageGenerationConfig(): ImageGenerationConfig | null {
	try {
		const parsed = loadRuntimeConfig();
		if (!parsed) return null;
		const image = parsed.imageGeneration;
		if (!image || typeof image !== "object") return null;
		const record = image as Record<string, unknown>;
		if (record.enabled !== true || typeof record.endpoint !== "string") return null;
		const endpoint = new URL(record.endpoint);
		if ((endpoint.protocol !== "http:" && endpoint.protocol !== "https:") || endpoint.hostname === "") return null;
		if (typeof record.tokenFile !== "string" || !path.isAbsolute(record.tokenFile)) return null;
		if (typeof record.maxBytes !== "number" || !Number.isInteger(record.maxBytes) || record.maxBytes <= 0) return null;
		if (typeof record.timeoutSeconds !== "number" || !Number.isInteger(record.timeoutSeconds) || record.timeoutSeconds <= 0) return null;
		return { endpoint: endpoint.toString(), tokenFile: record.tokenFile, maxBytes: record.maxBytes, timeoutSeconds: record.timeoutSeconds };
	} catch {
		return null;
	}
}

function imageError(text: string): AgentToolResult<unknown> {
	return { content: [{ type: "text", text }], details: undefined, isError: true };
}

async function readBoundedBody(response: Response, maxBytes: number, label = "生图服务响应"): Promise<Buffer> {
	if (!response.body) throw new Error(`${label}格式错误`);
	const reader = response.body.getReader();
	const chunks: Buffer[] = [];
	let total = 0;
	while (true) {
		const { done, value } = await reader.read();
		if (done) break;
		total += value.byteLength;
		if (total > maxBytes) throw new Error(`${label}超过大小限制`);
		chunks.push(Buffer.from(value));
	}
	return Buffer.concat(chunks, total);
}

function imageType(data: Buffer): { ext: string; mime: string } | null {
	if (data.length >= 8 && data.subarray(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))) {
		return { ext: "png", mime: "image/png" };
	}
	if (data.length >= 3 && data[0] === 0xff && data[1] === 0xd8 && data[2] === 0xff) {
		return { ext: "jpg", mime: "image/jpeg" };
	}
	if (data.length >= 12 && data.toString("ascii", 0, 4) === "RIFF" && data.toString("ascii", 8, 12) === "WEBP") {
		return { ext: "webp", mime: "image/webp" };
	}
	return null;
}

function imageAlt(prompt: string): string {
	return Array.from(prompt.replace(/[\r\n\[\]()]/g, "")).slice(0, 40).join("") || "生成图片";
}

function timestamp(date: Date): string {
	const pad = (n: number) => String(n).padStart(2, "0");
	return `${date.getUTCFullYear()}${pad(date.getUTCMonth() + 1)}${pad(date.getUTCDate())}-${pad(date.getUTCHours())}${pad(date.getUTCMinutes())}${pad(date.getUTCSeconds())}`;
}

async function executeImageGeneration(
	config: ImageGenerationConfig,
	params: ImageGenerationInput,
	signal: AbortSignal | undefined,
	sequence: number,
): Promise<AgentToolResult<unknown>> {
	const controller = new AbortController();
	let cancelled = false;
	let timedOut = false;
	const onAbort = () => {
		cancelled = true;
		controller.abort();
	};
	if (signal?.aborted) return imageError("生图已取消");
	signal?.addEventListener("abort", onAbort, { once: true });
	const timer = setTimeout(() => {
		timedOut = true;
		controller.abort();
	}, config.timeoutSeconds * 1000);
	let temporary = "";
	try {
		// 令牌会续期轮换,每次调用都现读,不能缓存启动时的值。
		const token = readFileSync(config.tokenFile, "utf8").trim();
		if (!token) return imageError("生图令牌不可用");
		const body: Record<string, string> = { prompt: params.prompt };
		if (params.size !== undefined) body.size = params.size;
		if (params.quality !== undefined) body.quality = params.quality;
		const response = await fetch(config.endpoint, {
			method: "POST",
			headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
			body: JSON.stringify(body),
			signal: controller.signal,
		});
		const raw = await readBoundedBody(response, config.maxBytes);
		let payload: any;
		try {
			payload = JSON.parse(raw.toString("utf8"));
		} catch {
			return imageError("生图服务响应格式错误");
		}
		const message = typeof payload?.error?.message === "string" ? payload.error.message : "";
		if (!response.ok) {
			if (response.status === 409) return imageError("未配置生图模型,请让用户在 Agent 设置里配置");
			if (response.status === 429) return imageError("本次对话的生图次数已达上限");
			if (response.status === 400) return imageError(`生图参数错误:${message || "请求参数无效"}`);
			if (response.status === 502) return imageError(`生图失败:${message.slice(0, 300) || "上游服务失败"}`);
			return imageError("生图失败:服务暂时不可用");
		}
		const encoded = payload?.data?.b64_json;
		if (typeof encoded !== "string") return imageError("生图服务响应缺少图片");
		const data = Buffer.from(encoded, "base64");
		const type = imageType(data);
		if (!type) return imageError("生图服务返回了不支持的图片数据");
		const model = typeof payload.data.model === "string" ? payload.data.model : "未知";
		const site = typeof payload.data.site === "string" ? payload.data.site : "未知";
		const alt = imageAlt(params.prompt);
		const name = `${String(sequence).padStart(2, "0")}-${timestamp(new Date())}.${type.ext}`;
		const cwd = process.cwd();
		const realCwd = realpathSync(cwd);
		const imagesDir = path.join(cwd, "images");
		await mkdir(imagesDir, { recursive: true });
		if (!isInside(realCwd, realpathSync(imagesDir))) return imageError("图片目录不在会话工作区内,已拒绝写入");
		const destination = path.join(imagesDir, name);
		if (!isInside(realCwd, destination)) return imageError("图片路径不在会话工作区内,已拒绝写入");
		const relative = path.posix.join("images", name);
		temporary = `${destination}.tmp-${process.pid}-${sequence}`;
		await writeFile(temporary, data, { flag: "wx" });
		if (signal?.aborted) throw new Error("aborted");
		await rename(temporary, destination);
		temporary = "";
		const attempts = Array.isArray(payload.data.attempts) ? payload.data.attempts.length : 0;
		const fallback = attempts > 0 ? `\n此前有 ${attempts} 次兜底` : "";
		return {
			content: [{
				type: "text",
				text: `已生成图片:${relative}(模型:${model}@${site})。请在回复中用下面的 Markdown 原样展示该图片(图片内容不会返回给你):\n![${alt}](${relative})${fallback}`,
			}],
			details: { images: [{ path: relative, mime: type.mime, bytes: data.length, model, site, alt }] },
		};
	} catch (error) {
		if (cancelled || signal?.aborted) return imageError("生图已取消");
		if (timedOut) return imageError("生图请求超时");
		if (error instanceof Error && /^(生图服务响应|图片)/.test(error.message)) return imageError(error.message);
		return imageError("生图请求失败:网络或服务不可用");
	} finally {
		clearTimeout(timer);
		signal?.removeEventListener("abort", onAbort);
		if (temporary) await rm(temporary, { force: true }).catch(() => {});
	}
}

function registerImageGeneration(pi: ExtensionAPI, config: ImageGenerationConfig) {
	let sequence = 0;
	pi.registerTool({
		name: "image_generation",
		label: "Generate image",
		description:
			"Generate an image from a detailed text prompt. The image is saved in the `images/` folder of the workspace and the tool result gives you a Markdown image reference: include it verbatim in your reply so the user sees the image. You never receive the image data.",
		parameters: Type.Object({
			prompt: Type.String({ minLength: 1, maxLength: 4000, description: "Detailed description of the image to generate" }),
			size: Type.Optional(Type.Union([
				Type.Literal("1024x1024"), Type.Literal("1536x1024"), Type.Literal("1024x1536"), Type.Literal("auto"),
			])),
			quality: Type.Optional(Type.Union([
				Type.Literal("low"), Type.Literal("medium"), Type.Literal("high"), Type.Literal("auto"),
			])),
		}),
		executionMode: "sequential",
		execute: async (_toolCallId, params, signal) => {
			sequence += 1;
			return executeImageGeneration(config, params, signal, sequence);
		},
	});
}

// ---- 文件摘要工具:经本 run 令牌把文件前 256 KiB 交给快速模型 ----
type FastTaskConfig = { endpoint: string; tokenFile: string; model: string; maxBytes: number; timeoutSeconds: number };
type FastTaskInput = { path: string; focus?: string };

// 只提供文件的前 256 KiB,更大的文件截断
const SUMMARIZE_READ_BYTES = 256 * 1024;
// 前 8 KiB 含 NUL 视为二进制文件
const SUMMARIZE_SNIFF_BYTES = 8 * 1024;
const SUMMARIZE_FOOTER = "(由快速模型摘要,可能遗漏细节;需要精确内容请用 read。)";

// 读不到、解析失败、字段非法 → 一律按未启用(fail-closed),校验方式同生图,
// 额外要求 model 为非空字符串(由 Go 侧在运行配置里下发)。
function loadFastTaskConfig(): FastTaskConfig | null {
	try {
		const parsed = loadRuntimeConfig();
		if (!parsed) return null;
		const fastTask = parsed.fastTask;
		if (!fastTask || typeof fastTask !== "object") return null;
		const record = fastTask as Record<string, unknown>;
		if (record.enabled !== true || typeof record.endpoint !== "string") return null;
		const endpoint = new URL(record.endpoint);
		if ((endpoint.protocol !== "http:" && endpoint.protocol !== "https:") || endpoint.hostname === "") return null;
		if (typeof record.tokenFile !== "string" || !path.isAbsolute(record.tokenFile)) return null;
		if (typeof record.model !== "string" || record.model.trim() === "") return null;
		if (typeof record.maxBytes !== "number" || !Number.isInteger(record.maxBytes) || record.maxBytes <= 0) return null;
		if (typeof record.timeoutSeconds !== "number" || !Number.isInteger(record.timeoutSeconds) || record.timeoutSeconds <= 0) return null;
		return { endpoint: endpoint.toString(), tokenFile: record.tokenFile, model: record.model, maxBytes: record.maxBytes, timeoutSeconds: record.timeoutSeconds };
	} catch {
		return null;
	}
}

function fastTaskError(text: string): AgentToolResult<unknown> {
	return { content: [{ type: "text", text }], details: undefined, isError: true };
}

async function executeSummarizeFile(
	config: FastTaskConfig,
	readRoots: string[],
	params: FastTaskInput,
	signal: AbortSignal | undefined,
	cwd: string | undefined,
): Promise<AgentToolResult<unknown>> {
	const controller = new AbortController();
	let cancelled = false;
	let timedOut = false;
	const onAbort = () => {
		cancelled = true;
		controller.abort();
	};
	if (signal?.aborted) return fastTaskError("文件摘要已取消");
	signal?.addEventListener("abort", onAbort, { once: true });
	const timer = setTimeout(() => {
		timedOut = true;
		controller.abort();
	}, config.timeoutSeconds * 1000);
	try {
		// 防御性校验(tool_call 层已查过一次):realpath + isInside,
		// 只允许工作目录与 readRoots,realpath 之后的真实路径不得逃逸。
		const dir = cwd || process.cwd();
		const realCwd = realpathSync(dir);
		const abs = toAbsolute(params.path, dir);
		if (abs === null) return fastTaskError("路径不在会话工作目录内,已拒绝访问");
		const target = resolveReal(abs);
		if (!isInside(realCwd, target) && !readRoots.some((root) => isInside(root, target))) {
			return fastTaskError("路径不在会话工作目录内,已拒绝访问");
		}

		let content: string;
		let truncated = false;
		let totalBytes = 0;
		try {
			const fd = openSync(target, "r");
			try {
				const stats = fstatSync(fd);
				if (!stats.isFile()) return fastTaskError("不是普通文件,无法摘要");
				const want = Math.min(stats.size, SUMMARIZE_READ_BYTES);
				const buffer = Buffer.alloc(want);
				const read = readSync(fd, buffer, 0, want, 0);
				const data = buffer.subarray(0, read);
				// 前 8 KiB 含 NUL 视为二进制,不把内容发给后端
				if (data.subarray(0, Math.min(read, SUMMARIZE_SNIFF_BYTES)).includes(0)) {
					return fastTaskError("不是文本文件,无法摘要");
				}
				truncated = stats.size > read;
				totalBytes = stats.size;
				content = data.toString("utf8");
			} finally {
				closeSync(fd);
			}
		} catch {
			return fastTaskError("无法读取文件");
		}

		let instruction = `用户关注点:${params.focus?.trim() || "概括文件的主要内容与结构"}`;
		if (truncated) instruction += `;文件共 ${totalBytes} 字节,仅提供前 256 KiB`;

		// 令牌每次现读,带 AbortSignal 与超时,响应有界读取
		const token = readFileSync(config.tokenFile, "utf8").trim();
		if (!token) return fastTaskError("文件摘要令牌不可用");
		const response = await fetch(config.endpoint, {
			method: "POST",
			headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
			body: JSON.stringify({ model: config.model, instruction, content, path: params.path }),
			signal: controller.signal,
		});
		const raw = await readBoundedBody(response, config.maxBytes, "摘要服务响应");
		let payload: any;
		try {
			payload = JSON.parse(raw.toString("utf8"));
		} catch {
			return fastTaskError("摘要服务响应格式错误");
		}
		const message = typeof payload?.error?.message === "string" ? payload.error.message : "";
		if (!response.ok) {
			if (response.status === 429) return fastTaskError("本次对话的文件摘要次数已达上限");
			if (response.status === 400) return fastTaskError(`文件摘要参数错误:${message || "请求参数无效"}`);
			if (response.status === 502) return fastTaskError("文件摘要失败:上游服务失败");
			return fastTaskError("文件摘要失败:服务暂时不可用");
		}
		const summary = payload?.data?.text;
		if (typeof summary !== "string" || summary.trim() === "") return fastTaskError("摘要服务未返回内容");
		const lines = [summary.trimEnd()];
		if (truncated) lines.push("注:文件较大,仅摘要了前 256 KiB 内容。");
		lines.push(SUMMARIZE_FOOTER);
		return { content: [{ type: "text", text: lines.join("\n") }] };
	} catch (error) {
		if (cancelled || signal?.aborted) return fastTaskError("文件摘要已取消");
		if (timedOut) return fastTaskError("文件摘要请求超时");
		if (error instanceof Error && /^(摘要服务响应)/.test(error.message)) return fastTaskError(error.message);
		return fastTaskError("文件摘要请求失败:网络或服务不可用");
	} finally {
		clearTimeout(timer);
		signal?.removeEventListener("abort", onAbort);
	}
}

function registerSummarizeFile(pi: ExtensionAPI, config: FastTaskConfig, readRoots: string[]) {
	pi.registerTool({
		name: "summarize_file",
		label: "Summarize file",
		description:
			"Summarize the content of a file with a fast model. Use it for large files (logs, long documents) when you only need the gist or a specific question answered. The summary comes from a smaller model and may miss details: use `read` or `grep` when you need exact content, and never rely on it to make edits.",
		parameters: Type.Object({
			path: Type.String({ minLength: 1, description: "Path of the file to summarize" }),
			focus: Type.Optional(Type.String({ maxLength: 500, description: "What you want to learn from the file (max 500 chars)" })),
		}),
		executionMode: "parallel",
		execute: async (_toolCallId, params, signal, _onUpdate, ctx) =>
			executeSummarizeFile(config, readRoots, params, signal, ctx?.cwd),
	});
}

// ---- question 工具:经 editor 对话向用户提问,回答作为工具结果回给模型 ----
type QuestionOption = { label: string; description?: string };
type QuestionItem = { header: string; question: string; multiSelect: boolean; options: QuestionOption[] };
type QuestionAnswer = { selected: string[]; other?: string };

const QUESTION_MAX_CALLS = 5;
// 「其他」由前端固定追加,模型不得提供同名选项;selected 里用 __other__ 标记选中
const OTHER_MARKER = "__other__";
const RESERVED_LABELS = new Set(["其他", "other", OTHER_MARKER]);

function questionError(text: string): AgentToolResult<unknown> {
	return { content: [{ type: "text", text }], details: undefined, isError: true };
}

function validateQuestionInput(questions: QuestionItem[]): string | null {
	if (!Array.isArray(questions) || questions.length < 1 || questions.length > 4) return "questions 必须是 1–4 个问题的数组";
	for (const q of questions) {
		if (!q || typeof q !== "object") return "每个问题必须是对象";
		if (typeof q.header !== "string" || q.header.length < 1 || q.header.length > 12) return "header 必须是 1–12 字的字符串";
		if (typeof q.question !== "string" || q.question.length < 1 || q.question.length > 300) return "question 必须是 1–300 字的字符串";
		if (typeof q.multiSelect !== "boolean") return "multiSelect 必须是布尔值";
		if (!Array.isArray(q.options) || q.options.length < 2 || q.options.length > 6) return "options 必须是 2–6 个选项的数组";
		const labels = new Set<string>();
		for (const option of q.options) {
			if (!option || typeof option !== "object") return "每个选项必须是对象";
			if (typeof option.label !== "string" || option.label.length < 1 || option.label.length > 60) return "选项 label 必须是 1–60 字的字符串";
			if (RESERVED_LABELS.has(option.label.trim().toLowerCase())) return "选项不得命名为「其他」或 Other,前端会固定追加";
			if (labels.has(option.label)) return `同一问题内选项 label 重复:${option.label}`;
			labels.add(option.label);
			if (option.description !== undefined && (typeof option.description !== "string" || option.description.length > 200)) {
				return "选项 description 必须是不超过 200 字的字符串";
			}
		}
	}
	return null;
}

// value 解析失败、结构非法、答案与题数不符等一律返回 null,按取消处理
function parseQuestionValue(value: string, questions: QuestionItem[]): { answers: QuestionAnswer[]; skipped: boolean } | null {
	let payload: unknown;
	try {
		payload = JSON.parse(value);
	} catch {
		return null;
	}
	if (!payload || typeof payload !== "object") return null;
	const record = payload as Record<string, unknown>;
	if (record.skipped === true) return { answers: [], skipped: true };
	if (!Array.isArray(record.answers) || record.answers.length !== questions.length) return null;
	const answers: QuestionAnswer[] = [];
	for (const [index, raw] of record.answers.entries()) {
		if (!raw || typeof raw !== "object") return null;
		const answer = raw as Record<string, unknown>;
		if (!Array.isArray(answer.selected)) return null;
		const selected: string[] = [];
		for (const label of answer.selected) {
			if (typeof label !== "string") return null;
			if (label !== OTHER_MARKER && !questions[index].options.some((option) => option.label === label)) return null;
			selected.push(label);
		}
		const hasOther = selected.includes(OTHER_MARKER);
		if (hasOther && (typeof answer.other !== "string" || answer.other.trim() === "")) return null;
		const parsed: QuestionAnswer = { selected };
		if (typeof answer.other === "string") parsed.other = answer.other;
		answers.push(parsed);
	}
	return { answers, skipped: false };
}

function formatQuestionAnswers(questions: QuestionItem[], answers: QuestionAnswer[]): string {
	const lines = questions.map((q, index) => {
		const answer = answers[index];
		const labels = answer.selected.filter((label) => label !== OTHER_MARKER);
		const parts: string[] = [];
		if (labels.length > 0) parts.push(labels.join("、"));
		if (answer.selected.includes(OTHER_MARKER)) parts.push(`其他:${answer.other ?? ""}`);
		return `${index + 1}. [${q.header}] ${q.question} → 选择:${parts.join(";")}`;
	});
	return `用户回答:\n${lines.join("\n")}`;
}

// Pi 1.4.0 的 RPC editor 忽略 signal,abort 时不会自动收口;扩展自行监听 signal
// 并 resolve undefined(按取消处理)。abort 后再到的迟到响应因 pending 请求已被
// Pi 清掉会被丢弃,不会误投给下一次 editor。
function editorWithAbort(
	ctx: ExtensionToolContext,
	toolCallId: string,
	questions: QuestionItem[],
	signal: AbortSignal | undefined,
): Promise<string | undefined> {
	if (signal?.aborted) return Promise.resolve(undefined);
	return new Promise((resolve) => {
		const onAbort = () => resolve(undefined);
		signal?.addEventListener("abort", onAbort, { once: true });
		ctx.ui
			.editor(`xlyra:question:${toolCallId}`, JSON.stringify({ toolCallId, questions }), { signal })
			.then(resolve, () => resolve(undefined))
			.finally(() => signal?.removeEventListener("abort", onAbort));
	});
}

function registerQuestion(pi: ExtensionAPI) {
	let calls = 0;
	pi.registerTool({
		name: "question",
		label: "Ask question",
		description:
			'Ask the user one or more multiple-choice questions when you need a decision or missing information. Use it only when you cannot reasonably decide yourself. Up to 4 questions; 2–6 options each; the user can always pick "Other" and type text. Do not ask unnecessary questions.',
		parameters: Type.Object({
			questions: Type.Array(
				Type.Object({
					header: Type.String({ minLength: 1, maxLength: 12 }),
					question: Type.String({ minLength: 1, maxLength: 300 }),
					multiSelect: Type.Boolean(),
					options: Type.Array(
						Type.Object({
							label: Type.String({ minLength: 1, maxLength: 60 }),
							description: Type.Optional(Type.String({ maxLength: 200 })),
						}),
						{ minItems: 2, maxItems: 6 },
					),
				}),
				{ minItems: 1, maxItems: 4 },
			),
		}),
		executionMode: "sequential",
		execute: async (toolCallId, params, signal, _onUpdate, ctx) => {
			const invalid = validateQuestionInput(params.questions);
			if (invalid !== null) return questionError(`提问参数非法:${invalid}`);
			calls += 1;
			if (calls > QUESTION_MAX_CALLS) return questionError("本轮提问次数已达上限,请自行判断");
			if (signal?.aborted) return questionError("用户取消了提问");
			const value = await editorWithAbort(ctx, toolCallId, params.questions, signal);
			if (typeof value !== "string") return questionError("用户取消了提问");
			const parsed = parseQuestionValue(value, params.questions);
			if (parsed === null) return questionError("用户取消了提问");
			if (parsed.skipped) {
				return {
					content: [{ type: "text", text: "用户选择不回答这些问题,请自行做合理假设并说明,或换个方式继续。" }],
					details: { questions: params.questions, answers: [], skipped: true },
				};
			}
			return {
				content: [{ type: "text", text: formatQuestionAnswers(params.questions, parsed.answers) }],
				details: { questions: params.questions, answers: parsed.answers, skipped: false },
			};
		},
	});
}

export default function (pi: ExtensionAPI) {
	const config = loadRunConfig();
	// provider 即原 models.json 的 providers.xlyra,字段名与 ProviderConfig 一致,原样注册;
	// 变量缺失时不注册(启动选模会报找不到模型)。
	if (config?.provider && typeof config.provider === "object") {
		pi.registerProvider("xlyra", config.provider as ProviderConfig);
	}
	// AGENTS.md 不再落盘:以 context file 注入,路径沿用原先 Pi 读取的
	// <PI_CODING_AGENT_DIR>/AGENTS.md,呈现格式与之前一致。Go 侧关闭时 agentsMd 为空。
	const agentsMd = config?.agentsMd;
	if (typeof agentsMd === "string" && agentsMd !== "") {
		const agentsPath = path.join(process.env.PI_CODING_AGENT_DIR || "", "AGENTS.md");
		pi.on("before_agent_start", (event) => {
			event.systemPromptOptions.contextFiles.push({ path: agentsPath, content: agentsMd });
		});
	}

	const permissionMode = loadPermissionMode();
	const readRoots = loadReadRoots();
	const bashConfig = loadBashConfig();
	const imageGenerationConfig = loadImageGenerationConfig();
	const fastTaskConfig = loadFastTaskConfig();

	if (imageGenerationConfig !== null) registerImageGeneration(pi, imageGenerationConfig);
	if (fastTaskConfig !== null) registerSummarizeFile(pi, fastTaskConfig, readRoots);
	registerQuestion(pi);

	if (bashConfig !== null) {
		const operations = bashConfig.mode === "local" ? localOperations(bashConfig) : sandboxOperations(bashConfig);
		// 覆盖内置 bash:工具名/描述/参数 schema/输出截断全部沿用,只换执行方式
		pi.registerTool({
			...createBashTool(process.cwd(), { operations }),
			label: bashConfig.mode === "local" ? "bash(本机)" : "bash(xlyra 沙箱)",
		});
		// 用户 ! 前缀与 RPC 的 bash 命令走同一套 operations;沙箱模式下不回退本地
		pi.on("user_bash", () => ({ operations }));
	}

	pi.on("tool_call", async (event, ctx) => {
		try {
			if (event.toolName === "image_generation" || event.toolName === "question") return; // 生图/提问不需要审批,也不接受路径参数
			if (event.toolName === "bash") {
				// 未启用:不做任何审批/拦截(此时由 Go 的 --tools 白名单保证 bash 不可用)
				if (bashConfig === null) return;
				return await approveBashCall(event, ctx, permissionMode);
			}
			if (!PATH_TOOLS.has(event.toolName)) return; // 其他工具放行,白名单由 Go 控制
			const input = (event.input ?? {}) as Record<string, unknown>;
			const rawPath = typeof input.path === "string" ? input.path : "";
			const cwd = ctx.cwd || process.cwd();
			const realCwd = realpathSync(cwd);

			if (rawPath === "") {
				if (OPTIONAL_PATH_TOOLS.has(event.toolName)) return; // 默认作用于 cwd
				return { block: true, reason: BLOCK_REASON_GENERIC }; // read/write/edit 缺 path,防御
			}

			const abs = toAbsolute(rawPath, cwd);
			if (abs === null) {
				return { block: true, reason: `路径不在会话工作目录内,已拒绝访问:${displayPath(rawPath)}` };
			}
			const target = resolveReal(abs);
			if (!isInside(realCwd, target)) {
				// 只读工具可放宽到 readRoots(技能目录);write/edit 即使落在 readRoots
				// 内也照旧拒绝,防止借技能目录改写宿主文件。
				const readonlyAllowed =
					READONLY_PATH_TOOLS.has(event.toolName) && readRoots.some((root) => isInside(root, target));
				if (!readonlyAllowed) {
					return { block: true, reason: `路径不在会话工作目录内,已拒绝访问:${displayPath(rawPath)}` };
				}
			}

			if (APPROVAL_TOOLS.has(event.toolName) && permissionMode === "ask") {
				const payload: Record<string, unknown> = {
					toolCallId: event.toolCallId,
					toolName: event.toolName,
					path: path.relative(realCwd, target) || ".",
				};
				const note = describeChange(event.toolName, input);
				if (note) payload.note = note;
				let confirmed = false;
				try {
					// 必须传 signal,否则等待审批时 abort 会被卡住(PoC T4.6 结论)
					confirmed = await ctx.ui.confirm("允许修改文件?", JSON.stringify(payload), {
						signal: ctx.signal,
					});
				} catch {
					confirmed = false; // abort 等异常一律按拒绝处理
				}
				if (!confirmed) return { block: true, reason: "用户拒绝了本次文件修改" };
			}
			return; // 放行
		} catch {
			return { block: true, reason: BLOCK_REASON_GENERIC };
		}
	});
}

import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanEnv } from "./env.mjs";
import { startMock } from "./mock-llm.mjs";
import { startRpc } from "./rpc-client.mjs";

// agent-runtime/ = test/lib/ 的上两级
const RUNTIME_DIR = fileURLToPath(new URL("../..", import.meta.url));
export const PI_ENTRY = join(
	RUNTIME_DIR,
	"node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js",
);
export const EXT_PATH = join(RUNTIME_DIR, "extension/xlyra.ts");
export const SECRET = "TOP-SECRET-CONTENT-9f3d";

function providerConfig(baseUrl) {
	return {
		baseUrl,
		api: "openai-completions",
		// 明文 key,避免引入额外的环境变量
		apiKey: "test-key",
		models: [
			{
				id: "mock-1",
				name: "mock-1",
				reasoning: false,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 128000,
				maxTokens: 2048,
			},
		],
	};
}

/**
 * 拉起一个完整的测试用例:独立临时目录 + 本机 mock LLM + 真实 Pi 进程。
 * provider 与运行配置统一经 XLYRA_RUN_CONFIG 传入,agentDir 里不写任何配置文件。
 *
 * options:
 *   name           用例名(临时目录前缀)
 *   permissionMode "ask" | "full"(写入 XLYRA_RUN_CONFIG.runtime)
 *   runtimeJson    直接指定 runtime 的原始 JSON 文本(优先于 permissionMode/readRoots;
 *                  不是合法 JSON 时原样作为字符串放进 runtime,测“配置非法”;
 *                  undefined 且未给 permissionMode 时不带 runtime,测“配置缺失”)
 *   readRoots      (caseDirs) => string[],在 prepare 之后求值并写入 runtime 的
 *                  readRoots 字段(目录需先落盘,扩展启动时才会 realpath)
 *   bash           沙箱 bash 配置,二选一:
 *                  - (caseDirs) => { socket, defaultTimeout?, maxTimeout? }:写入 runtime
 *                    的 bash 字段(自动补 enabled:true),并把 bash 加进 --tools
 *                  - true:只把 bash 加进 --tools,不写 bash 配置(测“未启用”行为)
 *                  socket 一般用 caseDirs.root 下的路径(套接字由假执行器监听)
 *   imageGeneration 生图配置对象或 (caseDirs) => 对象,自动补 enabled:true 与白名单
 *   agentsMd       写入 XLYRA_RUN_CONFIG.agentsMd 的 AGENTS.md 文本
 *   systemPrompt   非空时以 --system-prompt <文本> 传给 Pi
 *   prepare        ({root, home, agentDir, sessionDir, cwd, outside}) => 额外布置
 *   script         mock LLM 脚本 (req, n) => step
 *
 * 目录布局:root/{home,agent,sessions,cwd,outside};outside/secret.txt 是工作目录外的诱饵文件。
 */
export async function startCase({ name, permissionMode, runtimeJson, readRoots, bash, imageGeneration, agentsMd, systemPrompt, prepare, script }) {
	const root = mkdtempSync(join(tmpdir(), `xlyra-ext-${name}-`));
	const home = join(root, "home");
	const agentDir = join(root, "agent");
	const sessionDir = join(root, "sessions");
	const cwd = join(root, "cwd");
	const outside = join(root, "outside");
	for (const dir of [home, agentDir, sessionDir, cwd, outside]) mkdirSync(dir, { recursive: true });

	const mock = await startMock({ script: script || (() => ({ text: "ok" })) });
	const runConfig = { provider: providerConfig(`${mock.url}/v1`) };
	if (agentsMd !== undefined) runConfig.agentsMd = agentsMd;
	writeFileSync(join(outside, "secret.txt"), SECRET);
	if (prepare) prepare({ root, home, agentDir, sessionDir, cwd, outside });
	// readRoots 在 prepare 之后求值:引用的技能目录要先落盘
	const roots =
		typeof readRoots === "function"
			? readRoots({ root, home, agentDir, sessionDir, cwd, outside }) || []
			: undefined;
	// bash 配置同样在 prepare 之后求值:套接字放在用例临时目录里
	const bashOptions =
		typeof bash === "function" ? bash({ root, home, agentDir, sessionDir, cwd, outside }) : bash;
	const imageOptions =
		typeof imageGeneration === "function"
			? imageGeneration({ root, home, agentDir, sessionDir, cwd, outside })
			: imageGeneration;
	let runtimeConfig;
	if (runtimeJson !== undefined) {
		try {
			runtimeConfig = JSON.parse(runtimeJson);
		} catch {
			runtimeConfig = runtimeJson; // 非法 JSON 原样作为字符串,扩展应按缺失处理
		}
	} else if (permissionMode !== undefined || roots !== undefined || bashOptions !== undefined || imageOptions !== undefined) {
		const config = { permissionMode: permissionMode !== undefined ? permissionMode : "ask" };
		if (roots !== undefined) config.readRoots = roots;
		// bash:true 只把工具加进 --tools(不写配置),用于验证“未启用不覆盖”
		if (bashOptions !== undefined && bashOptions !== true) {
			config.bash = { enabled: true, ...bashOptions };
		}
		if (imageOptions !== undefined) config.imageGeneration = { enabled: true, ...imageOptions };
		runtimeConfig = config;
	}
	if (runtimeConfig !== undefined) runConfig.runtime = runtimeConfig;

	// question 无条件注册且不进生产白名单,这里统一放开供测试调用
	const tools = ["read", "ls", "grep", "find", "write", "edit", "question"];
	if (bash !== undefined) tools.push("bash");
	if (imageGeneration !== undefined) tools.push("image_generation");

	const client = startRpc({
		entry: PI_ENTRY,
		args: [
			"--mode",
			"rpc",
			"--session-dir",
			sessionDir,
			"--provider",
			"xlyra",
			"--model",
			"mock-1",
			"--tools",
			tools.join(","),
			"-e",
			EXT_PATH,
			// 与 PoC 一致的隔离旗标;--no-extensions 只关“发现型”扩展,不影响 -e 显式加载
			"--no-skills",
			"--no-context-files",
			"--no-mcp",
			"--no-extensions",
			...(systemPrompt ? ["--system-prompt", systemPrompt] : []),
		],
		// 环境变量按任务要求清空重建,只含 PATH/HOME/PI_CODING_AGENT_DIR/
		// PI_CODING_AGENT_SESSION_DIR/PI_OFFLINE/PI_SKIP_VERSION_CHECK/PI_TELEMETRY/XLYRA_RUN_CONFIG
		env: cleanEnv({ home, agentDir, sessionDir, extra: { XLYRA_RUN_CONFIG: JSON.stringify(runConfig) } }),
		cwd,
		stderrFile: join(root, "pi.stderr"),
		commandTimeoutMs: 20000,
	});

	const testcase = {
		name,
		root,
		home,
		agentDir,
		sessionDir,
		cwd,
		outside,
		mock,
		client,
		close: async () => {
			try {
				await client.close();
			} catch {
				// 进程退出失败由 startRpc 内部兜底 SIGKILL,这里不再上抛
			}
			try {
				await mock.close();
			} catch {
				// 端口可能已释放
			}
			rmSync(root, { recursive: true, force: true });
		},
	};
	return testcase;
}

/** mock 脚本:按顺序返回给定 step,之后统一回纯文本 */
export function steps(list) {
	return (_req, n) => {
		if (n <= list.length) return list[n - 1];
		return { text: "done" };
	};
}

export function sleep(ms) {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

/** 轮询断言用等待,超时抛错 */
export async function waitFor(predicate, timeoutMs, label) {
	const start = Date.now();
	while (Date.now() - start < timeoutMs) {
		const value = predicate();
		if (value) return value;
		await sleep(20);
	}
	throw new Error(`timeout waiting for ${label}`);
}

/** 第一个匹配 method 的 extension_ui_request 事件 */
export function uiRequest(client, method) {
	return client.events.find(
		(event) => event.type === "extension_ui_request" && event.method === method,
	);
}

export function uiRequests(client, method) {
	return client.events.filter(
		(event) => event.type === "extension_ui_request" && event.method === method,
	);
}

/** 自动应答所有 confirm 请求 */
export function autoApprove(client, confirmed = true) {
	client.on("extension_ui_request", (event) => {
		if (event.method === "confirm") client.extensionUiRespond(event.id, { confirmed });
	});
}

export function toolEnds(events) {
	return events.filter((event) => event.type === "tool_execution_end");
}

export function toolEnd(events, toolCallId) {
	return toolEnds(events).find((event) => event.toolCallId === toolCallId);
}

export function resultText(event) {
	const content = event?.result?.content;
	if (!Array.isArray(content)) return null;
	return content
		.filter((part) => part?.type === "text" && typeof part.text === "string")
		.map((part) => part.text)
		.join("\n");
}

/** mock 收到的 chat 请求(按顺序) */
export function chatRequests(mock) {
	return mock.requests.filter((record) => record.path.startsWith("/v1/chat/completions"));
}

/** 第 n 个(1 起)请求里模型看到的工具结果文本 */
export function toolResultTexts(mock, n) {
	const body = chatRequests(mock)[n - 1]?.body;
	if (!body || !Array.isArray(body.messages)) return [];
	return body.messages
		.filter((message) => message.role === "tool")
		.map((message) =>
			typeof message.content === "string" ? message.content : JSON.stringify(message.content),
		);
}

export function readFileOrNull(path) {
	try {
		return readFileSync(path, "utf8");
	} catch {
		return null;
	}
}

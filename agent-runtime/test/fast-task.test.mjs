import assert from "node:assert/strict";
import { test } from "node:test";
import http from "node:http";
import { existsSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
	chatRequests,
	resultText,
	startCase,
	steps,
	toolEnd,
	toolEnds,
	uiRequests,
	waitFor,
} from "./lib/harness.mjs";

const TIMEOUT = { timeout: 30000 };
const BLOCK_MARK = "路径不在会话工作目录内";
const FOOTER = "(由快速模型摘要,可能遗漏细节;需要精确内容请用 read。)";
const READ_LIMIT = 256 * 1024;

async function startFastTaskServer(handler) {
	const requests = [];
	const sockets = new Set();
	const server = http.createServer(async (req, res) => {
		let body = "";
		for await (const chunk of req) body += chunk;
		const record = { headers: req.headers, body: body ? JSON.parse(body) : {} };
		requests.push(record);
		await handler(record, res, requests.length);
	});
	server.on("connection", (socket) => {
		sockets.add(socket);
		socket.on("close", () => sockets.delete(socket));
	});
	await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
	const address = server.address();
	return {
		url: `http://127.0.0.1:${address.port}/internal/agent-llm/fast-task`,
		requests,
		close: async () => {
			for (const socket of sockets) socket.destroy();
			await new Promise((resolve) => server.close(resolve));
		},
	};
}

function respondText(res, text) {
	res.writeHead(200, { "Content-Type": "application/json" });
	res.end(JSON.stringify({ data: { text, model: "fast-mock" } }));
}

function fastTaskOptions(server) {
	return ({ agentDir }) => ({
		endpoint: server.url,
		tokenFile: join(agentDir, "token"),
		model: "fast-mock",
		maxBytes: 1024 * 1024,
		timeoutSeconds: 10,
	});
}

const prepareToken = ({ agentDir }) => writeFileSync(join(agentDir, "token"), "token-one\n");

function registeredTools(mock) {
	return chatRequests(mock)[0]?.body?.tools?.map((tool) => tool.function?.name ?? tool.name) ?? [];
}

test("仅在配置有效时注册 summarize_file,参数使用约束 schema", TIMEOUT, async () => {
	const server = await startFastTaskServer((_req, res) => respondText(res, "摘要"));
	const enabled = await startCase({
		name: "fast-registered",
		permissionMode: "ask",
		fastTask: fastTaskOptions(server),
		prepare: prepareToken,
	});
	try {
		await enabled.client.promptAndWait("你好");
		assert.ok(registeredTools(enabled.mock).includes("summarize_file"));
		const tool = chatRequests(enabled.mock)[0].body.tools.find((item) => (item.function?.name ?? item.name) === "summarize_file");
		const params = tool.function?.parameters ?? tool.parameters;
		assert.equal(params.properties.path.minLength, 1);
		assert.equal(params.properties.focus.maxLength, 500);
		assert.deepEqual(params.required, ["path"]);
	} finally {
		await enabled.close();
		await server.close();
	}

	const disabled = await startCase({ name: "fast-unregistered" });
	try {
		await disabled.client.promptAndWait("你好");
		assert.equal(registeredTools(disabled.mock).includes("summarize_file"), false);
	} finally {
		await disabled.close();
	}

	const invalid = await startCase({
		name: "fast-invalid-config",
		runtimeJson: JSON.stringify({ permissionMode: "full", fastTask: { enabled: true, endpoint: "file:///tmp/x", tokenFile: "relative", model: "", maxBytes: 1, timeoutSeconds: 1 } }),
		fastTask: {},
	});
	try {
		await invalid.client.promptAndWait("你好");
		assert.equal(registeredTools(invalid.mock).includes("summarize_file"), false);
	} finally {
		await invalid.close();
	}
});

test("成功摘要:默认关注点、截断提示与尾部说明", TIMEOUT, async () => {
	const server = await startFastTaskServer((_req, res) => respondText(res, "这是摘要正文。"));
	const c = await startCase({
		name: "fast-success",
		permissionMode: "full",
		fastTask: fastTaskOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [
			{ id: "small", name: "summarize_file", args: { path: "small.txt" } },
			{ id: "large", name: "summarize_file", args: { path: "large.log" } },
		] }]),
	});
	try {
		const small = "只有几行的小文件内容";
		const filler = "x".repeat(READ_LIMIT + 4096);
		writeFileSync(join(c.cwd, "small.txt"), small);
		writeFileSync(join(c.cwd, "large.log"), filler);
		const turn = await c.client.promptAndWait("摘要两个文件");
		assert.equal(server.requests.length, 2);

		assert.equal(server.requests[0].body.model, "fast-mock");
		assert.equal(server.requests[0].body.content, small);
		assert.equal(server.requests[0].body.instruction, "用户关注点:概括文件的主要内容与结构");
		assert.equal(server.requests[0].headers.authorization, "Bearer token-one");
		const smallEnd = toolEnd(turn.events, "small");
		assert.equal(smallEnd.isError, false);
		assert.deepEqual(resultText(smallEnd).split("\n"), ["这是摘要正文。", FOOTER]);

		// 截断:content 只送前 256 KiB,instruction 注明总字节数
		const large = server.requests[1].body;
		assert.equal(Buffer.byteLength(large.content, "utf8"), READ_LIMIT);
		assert.ok(large.instruction.includes(`文件共 ${READ_LIMIT + 4096} 字节,仅提供前 256 KiB`));
		assert.deepEqual(resultText(toolEnd(turn.events, "large")).split("\n"), [
			"这是摘要正文。",
			"注:文件较大,仅摘要了前 256 KiB 内容。",
			FOOTER,
		]);
	} finally {
		await c.close();
		await server.close();
	}
});

test("focus 透传到 instruction", TIMEOUT, async () => {
	const server = await startFastTaskServer((_req, res) => respondText(res, "摘要"));
	const c = await startCase({
		name: "fast-focus",
		permissionMode: "full",
		fastTask: fastTaskOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [{ id: "focus", name: "summarize_file", args: { path: "a.txt", focus: "错误出现的位置" } }] }]),
	});
	try {
		writeFileSync(join(c.cwd, "a.txt"), "文件内容");
		await c.client.promptAndWait("摘要");
		assert.equal(server.requests[0].body.instruction, "用户关注点:错误出现的位置");
	} finally {
		await c.close();
		await server.close();
	}
});

test("二进制文件(前 8 KiB 含 NUL)拒绝且不请求后端", TIMEOUT, async () => {
	const server = await startFastTaskServer((_req, res) => respondText(res, "摘要"));
	const c = await startCase({
		name: "fast-binary",
		permissionMode: "full",
		fastTask: fastTaskOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [
			{ id: "binary", name: "summarize_file", args: { path: "bin.dat" } },
		] }]),
	});
	try {
		writeFileSync(join(c.cwd, "bin.dat"), Buffer.concat([Buffer.from("head\u0000tail"), Buffer.alloc(64)]));
		const turn = await c.client.promptAndWait("摘要二进制");
		for (const id of ["binary"]) {
			assert.equal(toolEnd(turn.events, id).isError, true);
			assert.equal(resultText(toolEnd(turn.events, id)), "不是文本文件,无法摘要");
		}
		assert.equal(server.requests.length, 0);
	} finally {
		await c.close();
		await server.close();
	}
});

test("路径越界与符号链接逃逸被拒", TIMEOUT, async () => {
	const server = await startFastTaskServer((_req, res) => respondText(res, "摘要"));
	// mock 脚本在 prompt 时才被调用,此时 prepare 已把目录写进闭包
	let dirs = null;
	const script = (_req, n) =>
		n === 1
			? { toolCalls: [
					{ id: "abs", name: "summarize_file", args: { path: join(dirs.outside, "secret.txt") } },
					{ id: "rel", name: "summarize_file", args: { path: "../outside/secret.txt" } },
					{ id: "link", name: "summarize_file", args: { path: "leak.txt" } },
				] }
			: { text: "done" };
	const c = await startCase({
		name: "fast-paths",
		permissionMode: "full",
		fastTask: fastTaskOptions(server),
		prepare: ({ cwd, outside }) => {
			dirs = { cwd, outside };
			symlinkSync(join(outside, "secret.txt"), join(cwd, "leak.txt"));
		},
		script,
	});
	try {
		const turn = await c.client.promptAndWait("越界摘要", { timeoutMs: 15000 });
		assert.equal(toolEnds(turn.events).length, 3);
		for (const id of ["abs", "rel", "link"]) {
			const end = toolEnd(turn.events, id);
			assert.equal(end.isError, true, `${id} 应为错误结果`);
			assert.ok(resultText(end).includes(BLOCK_MARK), `${id} 结果应含拒绝说明:${resultText(end)}`);
		}
		// 目录外内容未被读出,后端未被请求
		assert.equal(server.requests.length, 0);
	} finally {
		await c.close();
		await server.close();
	}
});

test("后端 429/502/400 映射为简短中文且不泄露内部细节", TIMEOUT, async (t) => {
	const cases = [
		[429, "fast_task_quota_exceeded", "本次对话的文件摘要次数已达上限"],
		[502, "upstream_failed", "文件摘要失败:上游服务失败"],
		[400, "invalid_content", "文件摘要参数错误:content 过大"],
	];
	for (const [status, code, expected] of cases) {
		await t.test(String(status), async () => {
			const server = await startFastTaskServer((_req, res) => {
				res.writeHead(status, { "Content-Type": "application/json" });
				res.end(JSON.stringify({ error: { code, message: status === 400 ? "content 过大" : "" } }));
			});
			const c = await startCase({
				name: `fast-error-${status}`,
				permissionMode: "full",
				fastTask: fastTaskOptions(server),
				prepare: prepareToken,
				script: steps([{ toolCalls: [{ id: "error", name: "summarize_file", args: { path: "a.txt" } }] }]),
			});
			try {
				writeFileSync(join(c.cwd, "a.txt"), "内容");
				const turn = await c.client.promptAndWait("错误");
				const end = toolEnd(turn.events, "error");
				assert.equal(end.isError, true);
				assert.equal(resultText(end), expected);
				assert.equal(resultText(end).includes(server.url), false);
				assert.equal(resultText(end).includes("token-one"), false);
			} finally {
				await c.close();
				await server.close();
			}
		});
	}
});

test("每次调用现读轮换后的令牌", TIMEOUT, async () => {
	let tokenFile;
	const server = await startFastTaskServer((_req, res, count) => {
		respondText(res, "摘要");
		if (count === 1) writeFileSync(tokenFile, "token-two\n");
	});
	const c = await startCase({
		name: "fast-token-rotation",
		permissionMode: "full",
		fastTask: fastTaskOptions(server),
		prepare: ({ agentDir }) => {
			tokenFile = join(agentDir, "token");
			writeFileSync(tokenFile, "token-one\n");
		},
		script: steps([
			{ toolCalls: [{ id: "one", name: "summarize_file", args: { path: "a.txt" } }] },
			{ toolCalls: [{ id: "two", name: "summarize_file", args: { path: "a.txt" } }] },
		]),
	});
	try {
		writeFileSync(join(c.cwd, "a.txt"), "内容");
		await c.client.promptAndWait("两次摘要");
		assert.equal(server.requests.length, 2);
		assert.equal(server.requests[0].headers.authorization, "Bearer token-one");
		assert.equal(server.requests[1].headers.authorization, "Bearer token-two");
	} finally {
		await c.close();
		await server.close();
	}
});

test("取消中断请求且返回取消错误", TIMEOUT, async () => {
	const server = await startFastTaskServer(async (_req, res) => {
		await new Promise((resolve) => setTimeout(resolve, 10000));
		respondText(res, "摘要");
	});
	const c = await startCase({
		name: "fast-abort",
		permissionMode: "ask",
		fastTask: fastTaskOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [{ id: "abort", name: "summarize_file", args: { path: "a.txt" } }] }]),
	});
	try {
		writeFileSync(join(c.cwd, "a.txt"), "内容");
		const pending = c.client.promptAndWait("取消摘要");
		await waitFor(() => server.requests.length === 1, 5000, "摘要请求");
		const response = await c.client.send({ type: "abort" }, 5000);
		assert.equal(response.success, true);
		await pending;
	} finally {
		await c.close();
		await server.close();
	}
});

test("ask/full 模式都不触发审批", TIMEOUT, async (t) => {
	for (const permissionMode of ["ask", "full"]) {
		await t.test(permissionMode, async () => {
			const server = await startFastTaskServer((_req, res) => respondText(res, "摘要"));
			const c = await startCase({
				name: `fast-no-approval-${permissionMode}`,
				permissionMode,
				fastTask: fastTaskOptions(server),
				prepare: prepareToken,
				script: steps([{ toolCalls: [{ id: "sum", name: "summarize_file", args: { path: "a.txt" } }] }]),
			});
			try {
				writeFileSync(join(c.cwd, "a.txt"), "内容");
				const turn = await c.client.promptAndWait("直接摘要");
				assert.equal(toolEnd(turn.events, "sum").isError, false);
				assert.equal(uiRequests(c.client).length, 0);
				assert.equal(c.client.events.filter((event) => event.type === "extension_ui_request").length, 0);
			} finally {
				await c.close();
				await server.close();
			}
		});
	}
});

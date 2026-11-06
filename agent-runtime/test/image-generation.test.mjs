import assert from "node:assert/strict";
import { test } from "node:test";
import http from "node:http";
import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
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
const PNG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3]);
const JPEG = Buffer.from([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3]);
const WEBP = Buffer.from([...Buffer.from("RIFF"), 3, 0, 0, 0, ...Buffer.from("WEBP"), 1, 2, 3]);

async function startImageServer(handler) {
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
		url: `http://127.0.0.1:${address.port}/internal/agent-llm/images`,
		requests,
		close: async () => {
			for (const socket of sockets) socket.destroy();
			await new Promise((resolve) => server.close(resolve));
		},
	};
}

function respondImage(res, data, extra = {}) {
	res.writeHead(200, { "Content-Type": "application/json" });
	res.end(JSON.stringify({ data: { b64_json: data.toString("base64"), model: "gpt-image-2", site: "site-a", attempts: [], ...extra } }));
}

function imageOptions(server) {
	return ({ agentDir }) => ({
		endpoint: server.url,
		tokenFile: join(agentDir, "token"),
		maxBytes: 1024 * 1024,
		timeoutSeconds: 10,
	});
}

const prepareToken = ({ agentDir }) => writeFileSync(join(agentDir, "token"), "token-one\n");

function registeredTools(mock) {
	return chatRequests(mock)[0]?.body?.tools?.map((tool) => tool.function?.name ?? tool.name) ?? [];
}

test("仅在配置有效时注册 image_generation,参数使用约束 schema", TIMEOUT, async () => {
	const server = await startImageServer((_req, res) => respondImage(res, PNG));
	const enabled = await startCase({
		name: "image-registered",
		permissionMode: "ask",
		imageGeneration: imageOptions(server),
		prepare: prepareToken,
	});
	try {
		await enabled.client.promptAndWait("你好");
		const request = chatRequests(enabled.mock)[0].body;
		assert.ok(registeredTools(enabled.mock).includes("image_generation"));
		const tool = request.tools.find((item) => (item.function?.name ?? item.name) === "image_generation");
		const params = tool.function?.parameters ?? tool.parameters;
		assert.equal(params.properties.prompt.minLength, 1);
		assert.equal(params.properties.prompt.maxLength, 4000);
		assert.deepEqual(params.required, ["prompt"]);
		const enumValues = (schema) => schema.enum ?? schema.anyOf?.map((item) => item.const);
		assert.deepEqual(enumValues(params.properties.size), ["1024x1024", "1536x1024", "1024x1536", "auto"]);
		assert.deepEqual(enumValues(params.properties.quality), ["low", "medium", "high", "auto"]);
	} finally {
		await enabled.close();
		await server.close();
	}

	const disabled = await startCase({ name: "image-unregistered" });
	try {
		await disabled.client.promptAndWait("你好");
		assert.equal(registeredTools(disabled.mock).includes("image_generation"), false);
	} finally {
		await disabled.close();
	}

	const invalid = await startCase({
		name: "image-invalid-config",
		runtimeJson: JSON.stringify({ permissionMode: "full", imageGeneration: { enabled: true, endpoint: "file:///tmp/x", tokenFile: "relative", maxBytes: 1, timeoutSeconds: 1 } }),
		imageGeneration: {},
	});
	try {
		await invalid.client.promptAndWait("你好");
		assert.equal(registeredTools(invalid.mock).includes("image_generation"), false);
	} finally {
		await invalid.close();
	}
});

test("非法参数由 Pi 校验,不请求生图服务", TIMEOUT, async () => {
	const server = await startImageServer((_req, res) => respondImage(res, PNG));
	const c = await startCase({
		name: "image-params",
		permissionMode: "full",
		imageGeneration: imageOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [
			{ id: "empty", name: "image_generation", args: { prompt: "" } },
			{ id: "size", name: "image_generation", args: { prompt: "猫", size: "200x200" } },
			{ id: "quality", name: "image_generation", args: { prompt: "猫", quality: "best" } },
		] }]),
	});
	try {
		const turn = await c.client.promptAndWait("非法参数");
		assert.equal(toolEnds(turn.events).length, 3);
		for (const id of ["empty", "size", "quality"]) assert.equal(toolEnd(turn.events, id).isError, true);
		assert.equal(server.requests.length, 0);
		assert.equal(existsSync(join(c.cwd, "images")), false);
	} finally {
		await c.close();
		await server.close();
	}
});

test("PNG/JPEG/WebP 魔数落盘、固定文件名与 details 不含 base64", TIMEOUT, async () => {
	const server = await startImageServer((req, res) => {
		const data = req.body.prompt === "jpeg" ? JPEG : req.body.prompt === "webp" ? WEBP : PNG;
		respondImage(res, data, { attempts: req.body.prompt.startsWith("png") ? [{ model: "old", site: "old", error: "failed" }] : [] });
	});
	const c = await startCase({
		name: "image-success",
		permissionMode: "full",
		imageGeneration: imageOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [
			{ id: "png", name: "image_generation", args: { prompt: "png\n[一只](猫)", size: "1024x1024", quality: "high" } },
			{ id: "jpeg", name: "image_generation", args: { prompt: "jpeg" } },
			{ id: "webp", name: "image_generation", args: { prompt: "webp" } },
		] }]),
	});
	try {
		const turn = await c.client.promptAndWait("生成三张图");
		const files = readdirSync(join(c.cwd, "images")).sort();
		assert.equal(files.length, 3);
		for (const [index, ext] of ["png", "jpg", "webp"].entries()) {
			assert.match(files[index], new RegExp(`^0${index + 1}-\\d{8}-\\d{6}\\.${ext}$`));
		}
		assert.deepEqual(readFileSync(join(c.cwd, "images", files[0])), PNG);
		assert.deepEqual(readFileSync(join(c.cwd, "images", files[1])), JPEG);
		assert.deepEqual(readFileSync(join(c.cwd, "images", files[2])), WEBP);
		assert.deepEqual(server.requests[0].body, { prompt: "png\n[一只](猫)", size: "1024x1024", quality: "high" });
		for (const [index, id] of ["png", "jpeg", "webp"].entries()) {
			const end = toolEnd(turn.events, id);
			assert.equal(end.isError, false);
			assert.equal(end.result.details.images.length, 1);
			const detail = end.result.details.images[0];
			assert.equal(detail.path, `images/${files[index]}`);
			assert.ok(detail.bytes > 0);
			assert.equal(JSON.stringify(end.result.details).includes("b64_json"), false);
			assert.equal(JSON.stringify(end.result.details).includes(PNG.toString("base64")), false);
			assert.ok(resultText(end).includes(`![${detail.alt}](${detail.path})`));
		}
		assert.equal(toolEnd(turn.events, "png").result.details.images[0].alt, "png一只猫");
		assert.ok(resultText(toolEnd(turn.events, "png")).includes("此前有 1 次兜底"));
	} finally {
		await c.close();
		await server.close();
	}
});

test("非法图片数据拒绝且不落盘", TIMEOUT, async () => {
	const server = await startImageServer((_req, res) => respondImage(res, Buffer.from("not an image")));
	const c = await startCase({
		name: "image-invalid-data",
		permissionMode: "full",
		imageGeneration: imageOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [{ id: "bad", name: "image_generation", args: { prompt: "bad" } }] }]),
	});
	try {
		const turn = await c.client.promptAndWait("非法图片");
		assert.equal(toolEnd(turn.events, "bad").isError, true);
		assert.ok(resultText(toolEnd(turn.events, "bad")).includes("不支持的图片数据"));
		assert.equal(existsSync(join(c.cwd, "images")), false);
	} finally {
		await c.close();
		await server.close();
	}
});

test("HTTP 错误映射为简短中文且包含 400/502 服务端 message", TIMEOUT, async (t) => {
	const cases = [
		[409, "image_model_not_configured", "secret endpoint", "未配置生图模型,请让用户在 Agent 设置里配置"],
		[429, "image_quota_exceeded", "quota", "本次对话的生图次数已达上限"],
		[400, "invalid_prompt", "提示词不合规", "生图参数错误:提示词不合规"],
		[502, "upstream_failed", "上游超时", "生图失败:上游超时"],
	];
	for (const [status, code, message, expected] of cases) {
		await t.test(String(status), async () => {
			const server = await startImageServer((_req, res) => {
				res.writeHead(status, { "Content-Type": "application/json" });
				res.end(JSON.stringify({ error: { code, message } }));
			});
			const c = await startCase({
				name: `image-error-${status}`,
				permissionMode: "full",
				imageGeneration: imageOptions(server),
				prepare: prepareToken,
				script: steps([{ toolCalls: [{ id: "error", name: "image_generation", args: { prompt: "cat" } }] }]),
			});
			try {
				const turn = await c.client.promptAndWait("错误");
				const end = toolEnd(turn.events, "error");
				assert.equal(end.isError, true);
				assert.equal(resultText(end), expected);
				assert.equal(resultText(end).includes(server.url), false);
				assert.equal(existsSync(join(c.cwd, "images")), false);
			} finally {
				await c.close();
				await server.close();
			}
		});
	}
});

test("响应超过 maxBytes 拒绝", TIMEOUT, async () => {
	const server = await startImageServer((_req, res) => {
		res.writeHead(200, { "Content-Type": "application/json" });
		res.end(JSON.stringify({ data: { b64_json: PNG.toString("base64") }, padding: "x".repeat(2048) }));
	});
	const c = await startCase({
		name: "image-too-large",
		permissionMode: "full",
		imageGeneration: ({ agentDir }) => ({ ...imageOptions(server)({ agentDir }), maxBytes: 64 }),
		prepare: prepareToken,
		script: steps([{ toolCalls: [{ id: "large", name: "image_generation", args: { prompt: "cat" } }] }]),
	});
	try {
		const turn = await c.client.promptAndWait("超限");
		assert.equal(resultText(toolEnd(turn.events, "large")), "生图服务响应超过大小限制");
		assert.equal(existsSync(join(c.cwd, "images")), false);
	} finally {
		await c.close();
		await server.close();
	}
});

test("每次调用现读轮换后的令牌", TIMEOUT, async () => {
	let tokenFile;
	const server = await startImageServer((_req, res, count) => {
		respondImage(res, PNG);
		if (count === 1) writeFileSync(tokenFile, "token-two\n");
	});
	const c = await startCase({
		name: "image-token-rotation",
		permissionMode: "full",
		imageGeneration: imageOptions(server),
		prepare: ({ agentDir }) => {
			tokenFile = join(agentDir, "token");
			writeFileSync(tokenFile, "token-one\n");
		},
		script: steps([
			{ toolCalls: [{ id: "one", name: "image_generation", args: { prompt: "one" } }] },
			{ toolCalls: [{ id: "two", name: "image_generation", args: { prompt: "two" } }] },
		]),
	});
	try {
		await c.client.promptAndWait("两次生图");
		assert.equal(server.requests.length, 2);
		assert.equal(server.requests[0].headers.authorization, "Bearer token-one");
		assert.equal(server.requests[1].headers.authorization, "Bearer token-two");
	} finally {
		await c.close();
		await server.close();
	}
});

test("取消中断请求且不留临时文件或图片", TIMEOUT, async () => {
	const server = await startImageServer(async (_req, res) => {
		await new Promise((resolve) => setTimeout(resolve, 10000));
		respondImage(res, PNG);
	});
	const c = await startCase({
		name: "image-abort",
		permissionMode: "ask",
		imageGeneration: imageOptions(server),
		prepare: prepareToken,
		script: steps([{ toolCalls: [{ id: "abort", name: "image_generation", args: { prompt: "cat" } }] }]),
	});
	try {
		const pending = c.client.promptAndWait("取消生图");
		await waitFor(() => server.requests.length === 1, 5000, "生图请求");
		const response = await c.client.send({ type: "abort" }, 5000);
		assert.equal(response.success, true);
		await pending;
		assert.equal(existsSync(join(c.cwd, "images")), false);
	} finally {
		await c.close();
		await server.close();
	}
});

for (const permissionMode of ["ask", "full"]) {
	test(`${permissionMode}: 生图直接执行且不发任何审批请求`, TIMEOUT, async () => {
		const server = await startImageServer((_req, res) => respondImage(res, PNG));
		const c = await startCase({
			name: `image-no-approval-${permissionMode}`,
			permissionMode,
			imageGeneration: imageOptions(server),
			prepare: prepareToken,
			script: steps([{ toolCalls: [{ id: "image", name: "image_generation", args: { prompt: "cat" } }] }]),
		});
		try {
			const turn = await c.client.promptAndWait("直接生图");
			assert.equal(toolEnd(turn.events, "image").isError, false);
			assert.equal(uiRequests(c.client).length, 0);
			assert.equal(c.client.events.filter((event) => event.type === "extension_ui_request").length, 0);
		} finally {
			await c.close();
			await server.close();
		}
	});
}

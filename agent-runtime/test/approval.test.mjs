import assert from "node:assert/strict";
import { test } from "node:test";
import { existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
	SECRET,
	autoApprove,
	readFileOrNull,
	resultText,
	startCase,
	steps,
	toolEnd,
	toolEnds,
	toolResultTexts,
	uiRequest,
	uiRequests,
	waitFor,
} from "./lib/harness.mjs";
import { startFakeExec } from "./lib/fake-exec.mjs";

const TIMEOUT = { timeout: 30000 };

test("ask: write 审批通过后文件被写入且内容正确", TIMEOUT, async () => {
	const c = await startCase({
		name: "ask-write-allow",
		permissionMode: "ask",
		script: steps([
			{ toolCalls: [{ id: "call_w1", name: "write", args: { path: "out/hello.txt", content: "HELLO-XLYRA" } }] },
		]),
	});
	try {
		const pending = c.client.promptAndWait("写 out/hello.txt", { timeoutMs: 15000 });
		const request = await waitFor(() => uiRequest(c.client, "confirm"), 8000, "confirm 请求");
		assert.equal(request.type, "extension_ui_request");
		assert.equal(request.method, "confirm");
		assert.equal(request.title, "允许修改文件?");
		// message 必须是合法 JSON,且含 toolCallId / toolName / 相对 path / note
		const message = JSON.parse(request.message);
		assert.equal(message.toolCallId, "call_w1");
		assert.equal(message.toolName, "write");
		assert.equal(message.path, "out/hello.txt");
		assert.equal(message.note, "写入 11 字节");
		c.client.extensionUiRespond(request.id, { confirmed: true });
		const turn = await pending;

		const end = toolEnd(turn.events, "call_w1");
		assert.equal(end?.toolName, "write");
		assert.equal(end.isError, false);
		assert.equal(readFileOrNull(join(c.cwd, "out/hello.txt")), "HELLO-XLYRA");
		const settled = turn.events.find((event) => event.type === "agent_settled");
		assert.equal(settled?.aborted, false);
	} finally {
		await c.close();
	}
});

test("ask: write 审批拒绝后文件不存在,模型收到拒绝结果", TIMEOUT, async () => {
	const c = await startCase({
		name: "ask-write-deny",
		permissionMode: "ask",
		script: steps([
			{ toolCalls: [{ id: "call_w2", name: "write", args: { path: "out/denied.txt", content: "NOPE" } }] },
		]),
	});
	try {
		const pending = c.client.promptAndWait("写 out/denied.txt", { timeoutMs: 15000 });
		const request = await waitFor(() => uiRequest(c.client, "confirm"), 8000, "confirm 请求");
		c.client.extensionUiRespond(request.id, { confirmed: false });
		const turn = await pending;

		assert.equal(existsSync(join(c.cwd, "out/denied.txt")), false);
		const end = toolEnd(turn.events, "call_w2");
		assert.equal(end.isError, true);
		assert.equal(resultText(end), "用户拒绝了本次文件修改");
		// 下一次请求里模型收到的工具结果含“拒绝”
		const texts = toolResultTexts(c.mock, 2);
		assert.equal(texts.length, 1);
		assert.ok(texts[0].includes("用户拒绝了本次文件修改"), texts[0]);
	} finally {
		await c.close();
	}
});

test("ask: edit 审批通过后文件被修改", TIMEOUT, async () => {
	const c = await startCase({
		name: "ask-edit-allow",
		permissionMode: "ask",
		prepare: ({ cwd }) => writeFileSync(join(cwd, "edit-me.txt"), "alpha\nbeta\n"),
		script: steps([
			{
				toolCalls: [
					{
						id: "call_e1",
						name: "edit",
						args: { path: "edit-me.txt", edits: [{ oldText: "alpha", newText: "gamma" }] },
					},
				],
			},
		]),
	});
	try {
		const pending = c.client.promptAndWait("改 edit-me.txt", { timeoutMs: 15000 });
		const request = await waitFor(() => uiRequest(c.client, "confirm"), 8000, "confirm 请求");
		const message = JSON.parse(request.message);
		assert.equal(message.toolCallId, "call_e1");
		assert.equal(message.toolName, "edit");
		assert.equal(message.path, "edit-me.txt");
		assert.equal(message.note, "替换 1 处");
		c.client.extensionUiRespond(request.id, { confirmed: true });
		const turn = await pending;

		const end = toolEnd(turn.events, "call_e1");
		assert.equal(end.isError, false);
		assert.equal(readFileOrNull(join(c.cwd, "edit-me.txt")), "gamma\nbeta\n");
	} finally {
		await c.close();
	}
});

test("ask: edit 审批拒绝后文件保持原样", TIMEOUT, async () => {
	const c = await startCase({
		name: "ask-edit-deny",
		permissionMode: "ask",
		prepare: ({ cwd }) => writeFileSync(join(cwd, "edit-me.txt"), "alpha\nbeta\n"),
		script: steps([
			{
				toolCalls: [
					{
						id: "call_e2",
						name: "edit",
						args: { path: "edit-me.txt", edits: [{ oldText: "alpha", newText: "gamma" }] },
					},
				],
			},
		]),
	});
	try {
		const pending = c.client.promptAndWait("改 edit-me.txt", { timeoutMs: 15000 });
		const request = await waitFor(() => uiRequest(c.client, "confirm"), 8000, "confirm 请求");
		c.client.extensionUiRespond(request.id, { cancelled: true });
		const turn = await pending;

		const end = toolEnd(turn.events, "call_e2");
		assert.equal(end.isError, true);
		assert.equal(resultText(end), "用户拒绝了本次文件修改");
		assert.equal(readFileOrNull(join(c.cwd, "edit-me.txt")), "alpha\nbeta\n");
	} finally {
		await c.close();
	}
});

test("ask: 等待审批时 abort,3s 内收口且文件未写入,进程仍可回应", TIMEOUT, async () => {
	const c = await startCase({
		name: "ask-abort",
		permissionMode: "ask",
		script: steps([
			{ toolCalls: [{ id: "call_a1", name: "write", args: { path: "out/aborted.txt", content: "X" } }] },
		]),
	});
	try {
		const pending = c.client.promptAndWait("写 out/aborted.txt", { timeoutMs: 15000 });
		const request = await waitFor(() => uiRequest(c.client, "confirm"), 8000, "confirm 请求");
		const abortStarted = Date.now();
		const abortResponse = await c.client.send({ type: "abort" }, 10000);
		assert.equal(abortResponse.success, true);
		const turn = await pending; // agent_settled 到达即返回
		const elapsedMs = Date.now() - abortStarted;

		assert.ok(elapsedMs <= 3000, `abort 到 settled 用时 ${elapsedMs}ms`);
		const settled = turn.events.find((event) => event.type === "agent_settled");
		assert.equal(settled?.aborted, true);
		assert.equal(existsSync(join(c.cwd, "out/aborted.txt")), false);
		const end = toolEnd(turn.events, "call_a1");
		assert.equal(end?.isError, true);
		// 进程未被拖死,仍能回应 RPC
		const state = await c.client.send({ type: "get_state" }, 10000);
		assert.equal(state.success, true);
	} finally {
		await c.close();
	}
});

test("full: write/edit 不产生审批请求,直接成功", TIMEOUT, async () => {
	const c = await startCase({
		name: "full-no-approval",
		permissionMode: "full",
		prepare: ({ cwd }) => writeFileSync(join(cwd, "edit-me.txt"), "alpha\n"),
		script: steps([
			{
				toolCalls: [
					{ id: "call_f1", name: "write", args: { path: "out/full.txt", content: "FULL-MODE" } },
					{
						id: "call_f2",
						name: "edit",
						args: { path: "edit-me.txt", edits: [{ oldText: "alpha", newText: "delta" }] },
					},
				],
			},
		]),
	});
	try {
		const turn = await c.client.promptAndWait("直接写两个文件", { timeoutMs: 15000 });
		await new Promise((resolve) => setTimeout(resolve, 200));

		// 任何方法的 extension_ui_request 都不允许出现
		assert.equal(uiRequests(c.client).length, 0);
		assert.equal(c.client.events.filter((event) => event.type === "extension_ui_request").length, 0);
		const endW = toolEnd(turn.events, "call_f1");
		const endE = toolEnd(turn.events, "call_f2");
		assert.equal(endW?.isError, false);
		assert.equal(endE?.isError, false);
		assert.equal(readFileOrNull(join(c.cwd, "out/full.txt")), "FULL-MODE");
		assert.equal(readFileOrNull(join(c.cwd, "edit-me.txt")), "delta\n");
		assert.equal(toolEnds(turn.events).length, 2);
	} finally {
		await c.close();
	}
});

test("auto: write/edit 不触发审批,工作目录外的路径仍拒绝", TIMEOUT, async () => {
	const c = await startCase({
		name: "auto-write-edit-fast-path",
		permissionMode: "auto",
		prepare: ({ cwd }) => writeFileSync(join(cwd, "edit-me.txt"), "alpha\n"),
		script: steps([
			{
				toolCalls: [
					{ id: "call_aw", name: "write", args: { path: "out/auto.txt", content: "AUTO-MODE" } },
					{
						id: "call_ae",
						name: "edit",
						args: { path: "edit-me.txt", edits: [{ oldText: "alpha", newText: "auto" }] },
					},
					{ id: "call_aow", name: "write", args: { path: "../outside/denied.txt", content: "NOPE" } },
					{
						id: "call_aoe",
						name: "edit",
						args: { path: "../outside/secret.txt", edits: [{ oldText: SECRET, newText: "NOPE" }] },
					},
				],
			},
		]),
	});
	try {
		const turn = await c.client.promptAndWait("auto 写入并尝试越界", { timeoutMs: 15000 });
		assert.equal(uiRequests(c.client).length, 0);
		assert.equal(toolEnds(turn.events).length, 4);
		assert.equal(toolEnd(turn.events, "call_aw")?.isError, false);
		assert.equal(toolEnd(turn.events, "call_ae")?.isError, false);
		assert.equal(readFileOrNull(join(c.cwd, "out/auto.txt")), "AUTO-MODE");
		assert.equal(readFileOrNull(join(c.cwd, "edit-me.txt")), "auto\n");
		for (const id of ["call_aow", "call_aoe"]) {
			const end = toolEnd(turn.events, id);
			assert.equal(end?.isError, true);
			assert.ok(resultText(end).includes("路径不在会话工作目录内"), resultText(end));
		}
		assert.equal(existsSync(join(c.outside, "denied.txt")), false);
		assert.equal(readFileOrNull(join(c.outside, "secret.txt")), SECRET);
	} finally {
		await c.close();
	}
});

test("auto: bash 每条都审批,高危 note 带规则名,普通命令不带", TIMEOUT, async () => {
	const commands = [
		{ id: "call_auto_plain", command: "echo AUTO-PLAIN" },
		{ id: "call_auto_risky", command: "rm -rf /" },
		{ id: "call_auto_tail", command: "echo AUTO-TAIL" },
	];
	const c = await startCase({
		name: "auto-bash-approval",
		permissionMode: "auto",
		bash: ({ root }) => ({ socket: join(root, "exec.sock"), defaultTimeout: 30, maxTimeout: 60 }),
		script: steps([
			{ toolCalls: commands.map(({ id, command }) => ({ id, name: "bash", args: { command } })) },
		]),
	});
	const exec = await startFakeExec({ socketPath: join(c.root, "exec.sock"), behavior: () => ({ out: "ok\n" }) });
	autoApprove(c.client, true);
	try {
		const turn = await c.client.promptAndWait("auto 跑三条命令", { timeoutMs: 15000 });
		const requests = uiRequests(c.client, "confirm");
		assert.equal(requests.length, commands.length);
		const messages = new Map(requests.map((request) => {
			assert.equal(request.title, "允许执行命令?");
			const message = JSON.parse(request.message);
			return [message.toolCallId, message];
		}));
		assert.deepEqual([...messages.keys()].sort(), commands.map(({ id }) => id).sort());
		assert.equal(messages.get("call_auto_plain").note, "工作目录: .");
		assert.equal(messages.get("call_auto_tail").note, "工作目录: .");
		assert.equal(messages.get("call_auto_risky").note, "工作目录: .;命中规则: rm 递归删除高危目标");
		assert.ok(!messages.get("call_auto_plain").note.includes("命中规则"));
		assert.ok(!messages.get("call_auto_tail").note.includes("命中规则"));
		for (const { id } of commands) assert.equal(toolEnd(turn.events, id)?.isError, false);
		assert.equal(exec.requests.length, commands.length);
	} finally {
		await exec.close();
		await c.close();
	}
});

test("非法 permissionMode: fail-closed 回退 ask,write 仍需确认", TIMEOUT, async () => {
	const c = await startCase({
		name: "invalid-permission-mode-fallback",
		permissionMode: "yolo",
		script: steps([
			{ toolCalls: [{ id: "call_invalid", name: "write", args: { path: "fallback.txt", content: "ASK" } }] },
		]),
	});
	try {
		const pending = c.client.promptAndWait("非法模式下写文件", { timeoutMs: 15000 });
		const request = await waitFor(() => uiRequest(c.client, "confirm"), 8000, "confirm 请求");
		assert.equal(request.title, "允许修改文件?");
		c.client.extensionUiRespond(request.id, { confirmed: true });
		const turn = await pending;
		assert.equal(toolEnd(turn.events, "call_invalid")?.isError, false);
		assert.equal(readFileOrNull(join(c.cwd, "fallback.txt")), "ASK");
	} finally {
		await c.close();
	}
});

// 补充:ask 模式下 read 这类只读工具不应触发任何审批(避免误伤)
test("ask: read 不触发审批", TIMEOUT, async () => {
	const c = await startCase({
		name: "ask-read-no-approval",
		permissionMode: "ask",
		prepare: ({ cwd }) => writeFileSync(join(cwd, "data.txt"), "PLAIN-DATA"),
		script: steps([{ toolCalls: [{ id: "call_r1", name: "read", args: { path: "data.txt" } }] }]),
	});
	try {
		autoApprove(c.client, true); // 即使误发 confirm 也直接允许,好让用例继续暴露问题
		const turn = await c.client.promptAndWait("读 data.txt", { timeoutMs: 15000 });
		const end = toolEnd(turn.events, "call_r1");
		assert.equal(end?.isError, false);
		assert.ok(resultText(end).includes("PLAIN-DATA"));
		assert.equal(uiRequests(c.client).length, 0);
	} finally {
		await c.close();
	}
});

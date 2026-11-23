import assert from "node:assert/strict";
import { createServer } from "node:net";
import { rmSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { isPureXLyraCLICommand } from "../extension/xlyra.ts";
import { resultText, startCase, steps, toolEnd, uiRequests, waitFor } from "./lib/harness.mjs";
import { startFakeExec } from "./lib/fake-exec.mjs";

const TIMEOUT = { timeout: 30000 };
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

test("isPureXLyraCLICommand: 严格放行纯单条 xlyra 命令，拒绝旧名或复合/注入命令", () => {
	assert.equal(isPureXLyraCLICommand("xlyra status"), true);
	assert.equal(isPureXLyraCLICommand("  /usr/local/bin/xlyra config get general.log.level  "), true);
	assert.equal(isPureXLyraCLICommand("xlyra config set 'general.tasks.site_refresh_cron=0 */15 * * *'"), true);
	assert.equal(isPureXLyraCLICommand('xlyra proxy create --json \'{"name":"p1"}\''), true);

	assert.equal(isPureXLyraCLICommand(""), false);
	assert.equal(isPureXLyraCLICommand("xlyra-cli status"), false);
	assert.equal(isPureXLyraCLICommand("xlyra status && rm -rf /"), false);
	assert.equal(isPureXLyraCLICommand("xlyra status; id"), false);
	assert.equal(isPureXLyraCLICommand("xlyra status | cat"), false);
	assert.equal(isPureXLyraCLICommand("xlyra status > /tmp/x"), false);
	assert.equal(isPureXLyraCLICommand("xlyra status $(whoami)"), false);
	assert.equal(isPureXLyraCLICommand('xlyra site get "$(whoami)"'), false);
	assert.equal(isPureXLyraCLICommand("xlyra status `whoami`"), false);
	assert.equal(isPureXLyraCLICommand("FOO=bar xlyra status"), false);
	assert.equal(isPureXLyraCLICommand("xlyra status\necho hi"), false);
	assert.equal(isPureXLyraCLICommand("echo xlyra status"), false);
});

test("ask + ctlEnabled=true: 纯单条 xlyra 命令跳过 bash 弹窗直接执行", TIMEOUT, async () => {
	const command = "xlyra status";
	const c = await startCase({
		name: "ctl-bypass-pure",
		permissionMode: "ask",
		bash: (dirs) => ({ socket: join(dirs.root, "exec.sock"), defaultTimeout: 30, maxTimeout: 60, ctlEnabled: true }),
		script: steps([{ toolCalls: [{ id: "call_ctl_1", name: "bash", args: { command } }] }]),
	});
	const exec = await startFakeExec({
		socketPath: join(c.root, "exec.sock"),
		behavior: () => ({ out: '{"ok":true,"data":{"version":"v0.1.1"}}\n', code: 0 }),
	});
	try {
		const turn = await c.client.promptAndWait("检查状态", { timeoutMs: 15000 });
		assert.equal(uiRequests(c.client, "confirm").length, 0, "纯 xlyra 命令不应触发 bash confirm");
		assert.equal(exec.requests.length, 1);
		assert.equal(exec.requests[0].cmd, command);
		assert.equal(exec.requests[0].timeout, 60); // 自动按 maxTimeout 放宽
		const end = toolEnd(turn.events, "call_ctl_1");
		assert.equal(end?.isError, false);
		assert.ok(resultText(end).includes('"ok":true'), resultText(end));
	} finally {
		await exec.close();
		await c.close();
	}
});

test("ask + ctlEnabled=true: 复合命令仍触发 bash confirm 弹窗", TIMEOUT, async () => {
	const command = "xlyra status && echo INJECTED";
	const c = await startCase({
		name: "ctl-compound-confirm",
		permissionMode: "ask",
		bash: (dirs) => ({ socket: join(dirs.root, "exec.sock"), defaultTimeout: 30, maxTimeout: 60, ctlEnabled: true }),
		script: steps([{ toolCalls: [{ id: "call_ctl_2", name: "bash", args: { command } }] }]),
	});
	const exec = await startFakeExec({ socketPath: join(c.root, "exec.sock") });
	try {
		const pending = c.client.promptAndWait("跑复合命令", { timeoutMs: 15000 });
		const req = await waitFor(() => uiRequests(c.client, "confirm")[0], 8000, "compound command confirm");
		c.client.extensionUiRespond(req.id, { confirmed: false });
		const turn = await pending;
		assert.equal(exec.requests.length, 0, "被拒复合命令不得执行");
		assert.equal(toolEnd(turn.events, "call_ctl_2")?.isError, true);
	} finally {
		await exec.close();
		await c.close();
	}
});

test("ask + ctlEnabled 未开启: xlyra 仍按普通 bash 触发 confirm", TIMEOUT, async () => {
	const c = await startCase({
		name: "ctl-disabled-confirm",
		permissionMode: "ask",
		bash: (dirs) => ({ socket: join(dirs.root, "exec.sock"), defaultTimeout: 30, maxTimeout: 60 }),
		script: steps([{ toolCalls: [{ id: "call_ctl_3", name: "bash", args: { command: "xlyra status" } }] }]),
	});
	const exec = await startFakeExec({ socketPath: join(c.root, "exec.sock") });
	try {
		const pending = c.client.promptAndWait("执行", { timeoutMs: 15000 });
		const req = await waitFor(() => uiRequests(c.client, "confirm")[0], 8000, "normal bash confirm");
		c.client.extensionUiRespond(req.id, { confirmed: false });
		await pending;
		assert.equal(exec.requests.length, 0);
	} finally {
		await exec.close();
		await c.close();
	}
});

test("sandboxOperations: timer pause/resume 控制帧可在人工审阅期间暂停 3s+1s 兜底超时", TIMEOUT, async () => {
	const c = await startCase({
		name: "ctl-timer-pause-resume",
		permissionMode: "ask",
		bash: (dirs) => ({ socket: join(dirs.root, "exec.sock"), defaultTimeout: 1, maxTimeout: 1, ctlEnabled: true }),
		script: steps([{ toolCalls: [{ id: "call_ctl_pause", name: "bash", args: { command: "xlyra config set general.log.level=debug" } }] }]),
	});
	const sockPath = join(c.root, "exec.sock");
	const server = createServer((socket) => {
		let buf = "";
		socket.on("data", async (chunk) => {
			buf += chunk.toString("utf8");
			if (!buf.includes("\n")) return;
			// 先暂停计时，再等待 4.5s（超过 defaultTimeout(1s)+TIMEOUT_GRACE(3s)=4s）模拟人类审批
			socket.write(JSON.stringify({ t: "timer", action: "pause" }) + "\n");
			await sleep(4500);
			socket.write(JSON.stringify({ t: "timer", action: "resume" }) + "\n");
			socket.write(JSON.stringify({ t: "out", d: Buffer.from('{"ok":true}\n').toString("base64") }) + "\n");
			socket.write(JSON.stringify({ t: "exit", code: 0, signal: "", timed_out: false, reaped: 0, error: "" }) + "\n");
		});
	});
	await new Promise((resolve) => server.listen(sockPath, resolve));
	try {
		const turn = await c.client.promptAndWait("改日志级别", { timeoutMs: 20000 });
		const end = toolEnd(turn.events, "call_ctl_pause");
		assert.equal(end?.isError, false, `paused timer should not timeout, got: ${resultText(end)}`);
		assert.ok(resultText(end).includes('"ok":true'), resultText(end));
	} finally {
		server.closeAllConnections?.();
		await new Promise((resolve) => server.close(() => resolve()));
		rmSync(sockPath, { force: true });
		await c.close();
	}
});

import assert from "node:assert/strict";
import { test } from "node:test";
import { existsSync, mkdirSync, realpathSync, symlinkSync, writeFileSync } from "node:fs";
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
	uiRequests,
} from "./lib/harness.mjs";

const TIMEOUT = { timeout: 30000 };
const BLOCK_MARK = "路径不在会话工作目录内";

function prepareOutside({ cwd, outside }) {
	// 工作目录内的正常内容
	mkdirSync(join(cwd, "inside"), { recursive: true });
	writeFileSync(join(cwd, "inside", "data.txt"), "INSIDE-DATA-42");
	// 指向目录外的符号链接(目标存在)与悬空符号链接(目标不存在)
	symlinkSync(join(outside, "secret.txt"), join(cwd, "leak.txt"));
	symlinkSync(join(outside, "newfile.txt"), join(cwd, "dangle.txt"));
}

// 一轮把所有越界调用发出去:read 绝对路径 / ../ 逃逸 / ~/x / 符号链接 / @ 前缀,
// ls/grep/find 指向外部,write 目录外新文件与经由悬空链接的写入
function violationCalls(outside) {
	return [
		{ id: "r_abs", name: "read", args: { path: join(outside, "secret.txt") } },
		{ id: "r_rel", name: "read", args: { path: "../outside/secret.txt" } },
		{ id: "r_home", name: "read", args: { path: "~/x" } },
		{ id: "r_link", name: "read", args: { path: "leak.txt" } },
		{ id: "r_at", name: "read", args: { path: "@/etc/passwd" } },
		{ id: "l_out", name: "ls", args: { path: outside } },
		{ id: "g_out", name: "grep", args: { pattern: "SECRET", path: outside } },
		{ id: "f_out", name: "find", args: { pattern: "*", path: outside } },
		{ id: "w_out", name: "write", args: { path: join(outside, "new.txt"), content: "EVIL" } },
		{ id: "w_dangle", name: "write", args: { path: "dangle.txt", content: "EVIL2" } },
	];
}

for (const mode of ["ask", "full"]) {
	test(`路径约束(${mode}):工作目录外的读写全部被拒且不泄露内容`, TIMEOUT, async () => {
		// mock 脚本在 prompt 时才被调用,此时 prepare 已把目录写进 holder
		let dirs = null;
		const script = (_req, n) =>
			n === 1 ? { toolCalls: violationCalls(dirs.outside) } : { text: "done" };
		const c = await startCase({
			name: `paths-block-${mode}`,
			permissionMode: mode,
			prepare: (caseDirs) => {
				prepareOutside(caseDirs);
				dirs = caseDirs;
			},
			script,
		});
		try {
			const turn = await c.client.promptAndWait("尝试越界访问", { timeoutMs: 15000 });

			const ends = toolEnds(turn.events);
			assert.equal(ends.length, violationCalls(c.outside).length);
			for (const end of ends) {
				assert.equal(end.isError, true, `${end.toolCallId} 应为错误结果`);
				const text = resultText(end);
				assert.ok(text.includes(BLOCK_MARK), `${end.toolCallId} 结果应含拒绝说明:${text}`);
				// 拒绝理由不得回显 realpath 之后的宿主机真实路径
				assert.ok(!text.includes(realpathSync(c.cwd)), `reason 泄露真实路径:${text}`);
				assert.ok(!text.includes(realpathSync(c.outside)), `reason 泄露真实路径:${text}`);
			}

			// 目录外文件内容未被读出:工具结果与下一轮请求都不得包含秘密内容
			for (const end of ends) {
				assert.ok(!resultText(end).includes(SECRET), `${end.toolCallId} 泄露了目录外内容`);
			}
			const nextTexts = toolResultTexts(c.mock, 2);
			assert.equal(nextTexts.length, ends.length);
			for (const text of nextTexts) {
				assert.ok(text.includes(BLOCK_MARK), `模型收到的结果应含拒绝说明:${text}`);
				assert.ok(!text.includes(SECRET), "模型收到的结果泄露了目录外内容");
			}

			// 目录外未被写入(直接路径与悬空链接两条路都没走通)
			assert.equal(existsSync(join(c.outside, "new.txt")), false);
			assert.equal(existsSync(join(c.outside, "newfile.txt")), false);
			assert.equal(readFileOrNull(join(c.outside, "secret.txt")), SECRET);
			// 越界调用不该走到审批那一步
			assert.equal(uiRequests(c.client).length, 0);
			assert.equal(c.client.events.filter((event) => event.type === "extension_ui_request").length, 0);
			const settled = turn.events.find((event) => event.type === "agent_settled");
			assert.equal(settled?.aborted, false);
		} finally {
			await c.close();
		}
	});

	test(`路径约束(${mode}):工作目录内的读写正常放行`, TIMEOUT, async () => {
		const c = await startCase({
			name: `paths-allow-${mode}`,
			permissionMode: mode,
			prepare: prepareOutside,
			script: steps([
				{
					toolCalls: [
						{ id: "r_in", name: "read", args: { path: "inside/data.txt" } },
						{ id: "l_in", name: "ls", args: { path: "inside" } },
						{ id: "g_in", name: "grep", args: { pattern: "INSIDE", path: "inside" } },
						{ id: "f_in", name: "find", args: { pattern: "*.txt", path: "inside" } },
					],
				},
				{ text: "第一批完成" }, // 第一轮 prompt 到此收口
				{
					toolCalls: [
						{ id: "w_in", name: "write", args: { path: "inside/made.txt", content: "MADE-OK" } },
						{
							id: "e_in",
							name: "edit",
							args: { path: "inside/data.txt", edits: [{ oldText: "42", newText: "43" }] },
						},
					],
				},
			]),
		});
		try {
			if (mode === "ask") autoApprove(c.client, true);
			const first = await c.client.promptAndWait("读目录内文件", { timeoutMs: 15000 });
			const second = await c.client.promptAndWait("再写两个", { timeoutMs: 15000 });

			// read 正常返回内容
			const rIn = toolEnd(first.events, "r_in");
			assert.equal(rIn?.isError, false);
			assert.ok(resultText(rIn).includes("INSIDE-DATA-42"));
			// ls 正常
			const lIn = toolEnd(first.events, "l_in");
			assert.equal(lIn?.isError, false);
			// grep/find 未被扩展拦截(ripgrep/fd 在干净环境里可能缺失,但报错不能是路径拒绝)
			for (const id of ["g_in", "f_in"]) {
				const end = toolEnd(first.events, id);
				const text = resultText(end) ?? "";
				assert.ok(!text.includes(BLOCK_MARK), `${id} 不应被扩展拒绝:${text}`);
			}
			// 子目录内 write/edit 成功
			const wIn = toolEnd(second.events, "w_in");
			const eIn = toolEnd(second.events, "e_in");
			assert.equal(wIn?.isError, false);
			assert.equal(eIn?.isError, false);
			assert.equal(readFileOrNull(join(c.cwd, "inside", "made.txt")), "MADE-OK");
			assert.equal(readFileOrNull(join(c.cwd, "inside", "data.txt")), "INSIDE-DATA-43");

			if (mode === "ask") {
				// 只对 write/edit 触发审批,且 path 是相对路径
				const confirms = uiRequests(c.client, "confirm");
				assert.equal(confirms.length, 2);
				const paths = confirms.map((event) => JSON.parse(event.message).path).sort();
				assert.deepEqual(paths, ["inside/data.txt", "inside/made.txt"]);
			} else {
				assert.equal(c.client.events.filter((event) => event.type === "extension_ui_request").length, 0);
			}
		} finally {
			await c.close();
		}
	});
}

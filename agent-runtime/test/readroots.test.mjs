import assert from "node:assert/strict";
import { test } from "node:test";
import { existsSync, mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
	SECRET,
	readFileOrNull,
	resultText,
	startCase,
	toolEnd,
	toolEnds,
	uiRequests,
} from "./lib/harness.mjs";

const TIMEOUT = { timeout: 30000 };
const BLOCK_MARK = "路径不在会话工作目录内";
const SKILL_MD = "---\nname: pdf-tools\n---\nSKILL-PDF-CONTENT-77\n";

// 布置技能目录:SKILL.md + 待改文件 + 指向工作目录外的符号链接
function prepareSkillDir({ root, outside }) {
	const skillDir = join(root, "skills", "pdf-tools");
	mkdirSync(skillDir, { recursive: true });
	writeFileSync(join(skillDir, "SKILL.md"), SKILL_MD);
	writeFileSync(join(skillDir, "notes.txt"), "NOTES-ORIG");
	symlinkSync(join(outside, "secret.txt"), join(skillDir, "leak.txt"));
	return skillDir;
}

// 一轮发齐:读技能目录内四种工具 + 越界对照(链接逃逸/readRoot 外目录)+ 写类两种
function readRootCalls({ skillDir, outside }) {
	return [
		{ id: "r_skill", name: "read", args: { path: join(skillDir, "SKILL.md") } },
		{ id: "l_skill", name: "ls", args: { path: skillDir } },
		{ id: "g_skill", name: "grep", args: { pattern: "SKILL", path: skillDir } },
		{ id: "f_skill", name: "find", args: { pattern: "*", path: skillDir } },
		{ id: "r_link", name: "read", args: { path: join(skillDir, "leak.txt") } },
		{ id: "r_out", name: "read", args: { path: join(outside, "secret.txt") } },
		{ id: "w_skill", name: "write", args: { path: join(skillDir, "evil.txt"), content: "EVIL" } },
		{
			id: "e_skill",
			name: "edit",
			args: { path: join(skillDir, "notes.txt"), edits: [{ oldText: "ORIG", newText: "PWNED" }] },
		},
	];
}

for (const mode of ["ask", "full"]) {
	test(`readRoots(${mode}):技能目录只读放行,write/edit 仍被拒且零审批`, TIMEOUT, async () => {
		let dirs = null;
		const script = (_req, n) => (n === 1 ? { toolCalls: readRootCalls(dirs) } : { text: "done" });
		const c = await startCase({
			name: `readroots-${mode}`,
			permissionMode: mode,
			readRoots: (d) => [join(d.root, "skills", "pdf-tools")],
			prepare: (caseDirs) => {
				dirs = caseDirs;
				dirs.skillDir = prepareSkillDir(caseDirs);
			},
			script,
		});
		try {
			const turn = await c.client.promptAndWait("读技能目录", { timeoutMs: 15000 });

			const calls = readRootCalls(dirs);
			const ends = toolEnds(turn.events);
			assert.equal(ends.length, calls.length);

			// read 放行且内容真的被读出
			const rSkill = toolEnd(turn.events, "r_skill");
			assert.equal(rSkill?.isError, false);
			assert.ok(resultText(rSkill).includes("SKILL-PDF-CONTENT-77"), resultText(rSkill));
			// ls 放行;grep/find 只要不是路径拒绝即算放行(ripgrep/fd 可能缺失)
			assert.equal(toolEnd(turn.events, "l_skill")?.isError, false);
			for (const id of ["g_skill", "f_skill"]) {
				const text = resultText(toolEnd(turn.events, id)) ?? "";
				assert.ok(!text.includes(BLOCK_MARK), `${id} 不应被扩展拒绝:${text}`);
			}

			// 链接逃逸、readRoot 外目录、写类全部拒绝且不泄露内容
			for (const id of ["r_link", "r_out", "w_skill", "e_skill"]) {
				const end = toolEnd(turn.events, id);
				assert.equal(end?.isError, true, `${id} 应为错误结果`);
				const text = resultText(end);
				assert.ok(text.includes(BLOCK_MARK), `${id} 结果应含拒绝说明:${text}`);
				assert.ok(!text.includes(SECRET), `${id} 泄露了目录外内容`);
			}

			// 技能目录未被写入或修改
			assert.equal(existsSync(join(dirs.skillDir, "evil.txt")), false);
			assert.equal(readFileOrNull(join(dirs.skillDir, "notes.txt")), "NOTES-ORIG");
			// 写类在路径检查即被拒,不应走到审批
			assert.equal(uiRequests(c.client).length, 0);
			assert.equal(c.client.events.filter((event) => event.type === "extension_ui_request").length, 0);
			const settled = turn.events.find((event) => event.type === "agent_settled");
			assert.equal(settled?.aborted, false);
		} finally {
			await c.close();
		}
	});
}

test("readRoots 非法项全部忽略:/、深度 1、相对路径、非字符串、不存在的目录", TIMEOUT, async () => {
	let dirs = null;
	const script = (_req, n) =>
		n === 1
			? {
					toolCalls: [
						// "/" 与 "/etc"(macOS 上 realpath 为 /private/etc,靠原始深度拦)都被忽略时,
						// /etc/hosts 必须读不到
						{ id: "r_etc", name: "read", args: { path: "/etc/hosts" } },
						{ id: "r_out", name: "read", args: { path: join(dirs.outside, "secret.txt") } },
					],
				}
			: { text: "done" };
	const c = await startCase({
		name: "readroots-invalid",
		runtimeJson: JSON.stringify({
			permissionMode: "ask",
			readRoots: ["/", "/etc", "relative/skills", 42, true, "/no/such/deep/dir-xyz"],
		}),
		prepare: (caseDirs) => {
			dirs = caseDirs;
		},
		script,
	});
	try {
		const turn = await c.client.promptAndWait("读系统与目录外文件", { timeoutMs: 15000 });

		const ends = toolEnds(turn.events);
		assert.equal(ends.length, 2);
		for (const end of ends) {
			assert.equal(end.isError, true);
			const text = resultText(end);
			assert.ok(text.includes(BLOCK_MARK), `结果应含拒绝说明:${text}`);
			assert.ok(!text.includes(SECRET), "泄露了目录外内容");
			assert.ok(!text.includes("localhost"), "泄露了 /etc/hosts 内容");
		}
		assert.equal(uiRequests(c.client).length, 0);
	} finally {
		await c.close();
	}
});

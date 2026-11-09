import assert from "node:assert/strict";
import { test } from "node:test";
import { existsSync } from "node:fs";
import { join } from "node:path";
import { resultText, startCase, steps, toolEnd, toolEnds } from "./lib/harness.mjs";

const TIMEOUT = { timeout: 30000 };

test("白名单外工具:bash 调用返回 Tool bash not found 且无副作用", TIMEOUT, async () => {
	const c = await startCase({
		name: "whitelist-bash",
		permissionMode: "full",
		script: steps([
			{ toolCalls: [{ id: "call_b1", name: "bash", args: { command: "touch pwned.txt", timeout: 5 } }] },
		]),
	});
	try {
		const turn = await c.client.promptAndWait("执行 bash", { timeoutMs: 15000 });

		// 扩展不拦白名单外的工具名,但 --tools 已把 bash 排除,Pi 直接报 not found
		const end = toolEnd(turn.events, "call_b1");
		assert.equal(end?.toolName, "bash");
		assert.equal(end.isError, true);
		assert.equal(resultText(end), "Tool bash not found");
		assert.equal(existsSync(join(c.cwd, "pwned.txt")), false);
		assert.equal(toolEnds(turn.events).length, 1);
		// 不应产生任何审批请求
		assert.equal(c.client.events.filter((event) => event.type === "extension_ui_request").length, 0);
		const settled = turn.events.find((event) => event.type === "agent_settled");
		assert.equal(settled?.aborted, false);
	} finally {
		await c.close();
	}
});

import assert from "node:assert/strict";
import { test } from "node:test";
import { existsSync } from "node:fs";
import { join } from "node:path";
import { readFileOrNull, resultText, startCase, steps, toolEnd, uiRequest, waitFor } from "./lib/harness.mjs";

const TIMEOUT = { timeout: 30000 };

// 配置读不到 / 解析失败 / 值非法,都要按 ask 行为执行(write 触发审批)
const cases = [
	{ name: "config-missing", title: "配置缺失", xlyraJson: undefined, permissionMode: undefined },
	{ name: "config-invalid", title: "配置内容非法", xlyraJson: "{not-valid-json", permissionMode: "not-written" },
	{ name: "config-yolo", title: "permissionMode 非法值 yolo", xlyraJson: '{"permissionMode":"yolo"}', permissionMode: "not-written" },
];

for (const { name, title, xlyraJson, permissionMode } of cases) {
	test(`配置(${title})按 ask 处理:write 触发审批,拒绝后不写入`, TIMEOUT, async () => {
		const c = await startCase({
			name,
			permissionMode,
			xlyraJson,
			script: steps([
				{ toolCalls: [{ id: "call_c1", name: "write", args: { path: "out/cfg.txt", content: "CFG" } }] },
			]),
		});
		try {
			const pending = c.client.promptAndWait("写 out/cfg.txt", { timeoutMs: 15000 });
			const request = await waitFor(() => uiRequest(c.client, "confirm"), 8000, "confirm 请求");
			const message = JSON.parse(request.message);
			assert.equal(message.toolName, "write");
			assert.equal(message.path, "out/cfg.txt");
			c.client.extensionUiRespond(request.id, { confirmed: false });
			const turn = await pending;

			assert.equal(existsSync(join(c.cwd, "out/cfg.txt")), false);
			const end = toolEnd(turn.events, "call_c1");
			assert.equal(end.isError, true);
			assert.equal(resultText(end), "用户拒绝了本次文件修改");
		} finally {
			await c.close();
		}
	});
}

import assert from "node:assert/strict";
import { test } from "node:test";
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

const ONE_QUESTION = [
	{
		header: "方案",
		question: "选哪个方案?",
		multiSelect: false,
		options: [
			{ label: "A", description: "方案甲" },
			{ label: "B" },
		],
	},
];

function questionCall(id, questions = ONE_QUESTION) {
	return { id, name: "question", args: { questions } };
}

/** 等 editor 请求并按 transform 生成回答 JSON 回写 */
async function respondEditor(c, transform) {
	const request = await waitFor(() => uiRequests(c.client, "editor")[0], 8000, "editor 请求");
	const payload = JSON.parse(request.prefill);
	c.client.extensionUiRespond(request.id, { value: JSON.stringify(transform(payload)) });
	return request;
}

function registeredTools(mock) {
	return chatRequests(mock)[0]?.body?.tools?.map((tool) => tool.function?.name ?? tool.name) ?? [];
}

test("question 无条件注册,参数 schema 边界正确", TIMEOUT, async () => {
	const c = await startCase({ name: "question-registered", permissionMode: "ask" });
	try {
		await c.client.promptAndWait("你好");
		assert.ok(registeredTools(c.mock).includes("question"));
		const request = chatRequests(c.mock)[0].body;
		const tool = request.tools.find((item) => (item.function?.name ?? item.name) === "question");
		assert.equal(
			tool.function.description,
			'Ask the user one or more multiple-choice questions when you need a decision or missing information. Use it only when you cannot reasonably decide yourself. Up to 4 questions; 2–6 options each; the user can always pick "Other" and type text. Do not ask unnecessary questions.',
		);
		const params = tool.function.parameters;
		assert.deepEqual(params.required, ["questions"]);
		assert.equal(params.properties.questions.minItems, 1);
		assert.equal(params.properties.questions.maxItems, 4);
		const item = params.properties.questions.items;
		assert.equal(item.properties.header.maxLength, 12);
		assert.equal(item.properties.question.maxLength, 300);
		assert.equal(item.properties.multiSelect.type, "boolean");
		assert.deepEqual(item.required, ["header", "question", "multiSelect", "options"]);
		assert.equal(item.properties.options.minItems, 2);
		assert.equal(item.properties.options.maxItems, 6);
		const option = item.properties.options.items;
		assert.equal(option.properties.label.maxLength, 60);
		assert.equal(option.properties.description.maxLength, 200);
		assert.deepEqual(option.required, ["label"]);
	} finally {
		await c.close();
	}
});

test("重复 label / 保留名选项:参数非法,不发 editor 请求", TIMEOUT, async () => {
	const c = await startCase({
		name: "question-invalid-params",
		permissionMode: "full",
		script: steps([
			{
				toolCalls: [
					questionCall("dup", [
						{ header: "H", question: "Q", multiSelect: false, options: [{ label: "A" }, { label: "A" }] },
					]),
					questionCall("other-cn", [
						{ header: "H", question: "Q", multiSelect: false, options: [{ label: "A" }, { label: "其他" }] },
					]),
					questionCall("other-en", [
						{ header: "H", question: "Q", multiSelect: false, options: [{ label: "A" }, { label: " oThEr " }] },
					]),
					questionCall("other-marker", [
						{ header: "H", question: "Q", multiSelect: false, options: [{ label: "A" }, { label: "__OTHER__" }] },
					]),
				],
			},
		]),
	});
	try {
		const turn = await c.client.promptAndWait("非法参数");
		assert.equal(toolEnds(turn.events).length, 4);
		for (const id of ["dup", "other-cn", "other-en", "other-marker"]) {
			const end = toolEnd(turn.events, id);
			assert.equal(end.isError, true);
			assert.ok(resultText(end).startsWith("提问参数非法:"), resultText(end));
		}
		assert.equal(uiRequests(c.client).length, 0);
	} finally {
		await c.close();
	}
});

test("schema 边界:空数组/超量/超长由 Pi 拦截,不发 editor 请求", TIMEOUT, async () => {
	const c = await startCase({
		name: "question-schema-bounds",
		permissionMode: "full",
		script: steps([
			{
				toolCalls: [
					questionCall("empty", []),
					questionCall("five", Array.from({ length: 5 }, () => ONE_QUESTION[0])),
					questionCall("long-header", [{ ...ONE_QUESTION[0], header: "x".repeat(13) }]),
					questionCall("long-question", [{ ...ONE_QUESTION[0], question: "x".repeat(301) }]),
					questionCall("one-option", [{ ...ONE_QUESTION[0], options: [{ label: "A" }] }]),
					questionCall("seven-options", [
						{ ...ONE_QUESTION[0], options: Array.from({ length: 7 }, (_, i) => ({ label: `L${i}` })) },
					]),
					questionCall("long-label", [{ ...ONE_QUESTION[0], options: [{ label: "x".repeat(61) }, { label: "B" }] }]),
					questionCall("long-desc", [
						{ ...ONE_QUESTION[0], options: [{ label: "A", description: "x".repeat(201) }, { label: "B" }] },
					]),
				],
			},
		]),
	});
	try {
		const turn = await c.client.promptAndWait("边界参数");
		assert.equal(toolEnds(turn.events).length, 8);
		for (const end of toolEnds(turn.events)) assert.equal(end.isError, true);
		assert.equal(uiRequests(c.client).length, 0);
	} finally {
		await c.close();
	}
});

test("单选:editor 请求 title/prefill 形态与结果文字、details", TIMEOUT, async () => {
	const c = await startCase({
		name: "question-single",
		permissionMode: "ask",
		script: steps([{ toolCalls: [questionCall("call_q1")] }]),
	});
	try {
		const pending = c.client.promptAndWait("提问", { timeoutMs: 15000 });
		const request = await waitFor(() => uiRequests(c.client, "editor")[0], 8000, "editor 请求");
		assert.equal(request.title, "xlyra:question:call_q1");
		assert.deepEqual(JSON.parse(request.prefill), { toolCallId: "call_q1", questions: ONE_QUESTION });
		c.client.extensionUiRespond(request.id, {
			value: JSON.stringify({ answers: [{ selected: ["A"] }] }),
		});
		const turn = await pending;

		const end = toolEnd(turn.events, "call_q1");
		assert.equal(end.isError, false);
		assert.equal(resultText(end), "用户回答:\n1. [方案] 选哪个方案? → 选择:A");
		assert.deepEqual(end.result.details, {
			questions: ONE_QUESTION,
			answers: [{ selected: ["A"] }],
			skipped: false,
		});
		// ask 模式下 question 不触发任何 confirm
		assert.equal(uiRequests(c.client, "confirm").length, 0);
	} finally {
		await c.close();
	}
});

test("多选与「其他」:顿号连接、其他文本原样、只选其他", TIMEOUT, async () => {
	const questions = [
		{ header: "功能", question: "要哪些功能?", multiSelect: true, options: [{ label: "搜索" }, { label: "导出" }, { label: "分享" }] },
		{ header: "补充", question: "还有吗?", multiSelect: false, options: [{ label: "没有" }, { label: "有" }] },
	];
	const answers = [
		{ selected: ["搜索", "分享", "__other__"], other: "  定时任务\n换行保留  " },
		{ selected: ["__other__"], other: "自定义" },
	];
	const c = await startCase({
		name: "question-multi-other",
		permissionMode: "full",
		script: steps([{ toolCalls: [questionCall("call_q2", questions)] }]),
	});
	try {
		const pending = c.client.promptAndWait("提问", { timeoutMs: 15000 });
		await respondEditor(c, () => ({ answers }));
		const turn = await pending;

		const end = toolEnd(turn.events, "call_q2");
		assert.equal(end.isError, false);
		assert.equal(
			resultText(end),
			"用户回答:\n1. [功能] 要哪些功能? → 选择:搜索、分享;其他:  定时任务\n换行保留  \n2. [补充] 还有吗? → 选择:其他:自定义",
		);
		assert.deepEqual(end.result.details, { questions, answers, skipped: false });
		assert.equal(uiRequests(c.client, "confirm").length, 0);
	} finally {
		await c.close();
	}
});

test("跳过:固定文案,details.skipped=true", TIMEOUT, async () => {
	const c = await startCase({
		name: "question-skip",
		permissionMode: "ask",
		script: steps([{ toolCalls: [questionCall("call_q3")] }]),
	});
	try {
		const pending = c.client.promptAndWait("提问", { timeoutMs: 15000 });
		await respondEditor(c, () => ({ skipped: true }));
		const turn = await pending;

		const end = toolEnd(turn.events, "call_q3");
		assert.equal(end.isError, false);
		assert.equal(resultText(end), "用户选择不回答这些问题,请自行做合理假设并说明,或换个方式继续。");
		assert.deepEqual(end.result.details, { questions: ONE_QUESTION, answers: [], skipped: true });
	} finally {
		await c.close();
	}
});

test("取消与非法应答:一律 isError「用户取消了提问」", TIMEOUT, async (t) => {
	const cases = [
		["cancelled", null],
		["bad-json", "not json {"],
		["count-mismatch", JSON.stringify({ answers: [] })],
		["unknown-label", JSON.stringify({ answers: [{ selected: ["C"] }] })],
		["other-no-text", JSON.stringify({ answers: [{ selected: ["__other__"] }] })],
		["other-blank-text", JSON.stringify({ answers: [{ selected: ["__other__"], other: "  " }] })],
	];
	for (const [name, value] of cases) {
		await t.test(name, async () => {
			const c = await startCase({
				name: `question-${name}`,
				permissionMode: "full",
				script: steps([{ toolCalls: [questionCall("call_bad")] }]),
			});
			try {
				const pending = c.client.promptAndWait("提问", { timeoutMs: 15000 });
				const request = await waitFor(() => uiRequests(c.client, "editor")[0], 8000, "editor 请求");
				if (value === null) c.client.extensionUiRespond(request.id, { cancelled: true });
				else c.client.extensionUiRespond(request.id, { value });
				const turn = await pending;

				const end = toolEnd(turn.events, "call_bad");
				assert.equal(end.isError, true);
				assert.equal(resultText(end), "用户取消了提问");
			} finally {
				await c.close();
			}
		});
	}
});

test("次数上限:第 6 次调用直接报错且不发 editor 请求", TIMEOUT, async () => {
	const c = await startCase({
		name: "question-limit",
		permissionMode: "full",
		script: steps([
			{ toolCalls: [questionCall("q1"), questionCall("q2"), questionCall("q3"), questionCall("q4"), questionCall("q5"), questionCall("q6")] },
		]),
	});
	try {
		c.client.on("extension_ui_request", (event) => {
			if (event.method === "editor") {
				c.client.extensionUiRespond(event.id, { value: JSON.stringify({ answers: [{ selected: ["A"] }] }) });
			}
		});
		const turn = await c.client.promptAndWait("连续提问", { timeoutMs: 20000 });
		assert.equal(toolEnds(turn.events).length, 6);
		assert.equal(uiRequests(c.client, "editor").length, 5);
		for (const id of ["q1", "q2", "q3", "q4", "q5"]) assert.equal(toolEnd(turn.events, id).isError, false);
		const sixth = toolEnd(turn.events, "q6");
		assert.equal(sixth.isError, true);
		assert.equal(resultText(sixth), "本轮提问次数已达上限,请自行判断");
	} finally {
		await c.close();
	}
});

test("等待回答时 abort:3s 内收口,结果为取消,进程仍可回应", TIMEOUT, async () => {
	const c = await startCase({
		name: "question-abort",
		permissionMode: "ask",
		script: steps([{ toolCalls: [questionCall("call_abort")] }]),
	});
	try {
		const pending = c.client.promptAndWait("提问", { timeoutMs: 15000 });
		await waitFor(() => uiRequests(c.client, "editor")[0], 8000, "editor 请求");
		const abortStarted = Date.now();
		const response = await c.client.send({ type: "abort" }, 10000);
		assert.equal(response.success, true);
		const turn = await pending;
		assert.ok(Date.now() - abortStarted <= 3000);

		const settled = turn.events.find((event) => event.type === "agent_settled");
		assert.equal(settled?.aborted, true);
		const end = toolEnd(turn.events, "call_abort");
		assert.equal(end?.isError, true);
		assert.equal(resultText(end), "用户取消了提问");
		const state = await c.client.send({ type: "get_state" }, 10000);
		assert.equal(state.success, true);
	} finally {
		await c.close();
	}
});

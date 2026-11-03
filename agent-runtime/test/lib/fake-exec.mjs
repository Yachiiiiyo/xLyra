import { createServer } from "node:net";
import { rmSync } from "node:fs";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function sendLine(socket, object) {
	socket.write(JSON.stringify(object) + "\n");
}

/**
 * 协议假执行器:监听 Unix 套接字,按可编程 behavior 应答 xLyra 沙箱执行协议。
 * 不执行任何真实命令,只回协议帧。
 *
 * startFakeExec({ socketPath, behavior }) → { socketPath, requests, close }
 *   behavior(req) → plan(可异步;req 是解析后的请求对象)。plan 形态:
 *     { out?, err?, code? }            固定输出 + 退出码(缺省 code 0)
 *     { chunks: [{ text, delayMs? }], code? }  流式分块输出后退出
 *     { delayMs?, ... }                应答前先等待
 *     { hang: true } / undefined       收到请求后不应答(测超时/abort/客户端断开)
 *     { error: "..." }                 exit 带 error 字段
 *     { timedOut: true, code? }        exit 带 timed_out:true
 *     { raw: "..." }                   发送一行非法 JSON 后挂住
 *     { oversize: true }               发送超过 4 MiB 的单行(无换行)后挂住
 *     { noExit: true, out? }           发送部分输出后不发 exit 直接断开
 *
 * requests[] 记录收到的每个请求(cmd/cwd/timeout/env 等原样保留,附 closedByClient:
 * 服务器已发 exit/主动断开之前客户端就断开时为 true)。
 */
export async function startFakeExec({ socketPath, behavior = () => ({ out: "", code: 0 }) }) {
	const requests = [];
	let serverShuttingDown = false;

	const server = createServer((socket) => {
		// 客户端按协议主动断开(超时/abort/协议错误)时,剩余写入会 EPIPE 等,忽略即可
		socket.on("error", () => {});
		let buffer = "";
		let record = null;
		let settled = false; // 服务器已发 exit 或已主动断开:之后的 close 不算客户端提前断开

		socket.on("close", () => {
			if (record !== null && !settled && !serverShuttingDown) record.closedByClient = true;
		});

		socket.on("data", async (chunk) => {
			buffer += chunk.toString("utf8");
			const newlineAt = buffer.indexOf("\n");
			if (newlineAt === -1) return; // 请求必须一行发完
			const line = buffer.slice(0, newlineAt);
			let request;
			try {
				request = JSON.parse(line);
			} catch {
				request = { raw: line };
			}
			record = { closedByClient: false, ...request };
			requests.push(record);

			let plan;
			try {
				plan = await behavior(request);
			} catch {
				socket.destroy();
				return;
			}
			if (plan === undefined || plan === null || plan.hang) return; // 不应答

			if (plan.delayMs) await sleep(plan.delayMs);
			if (plan.raw !== undefined) {
				socket.write(`${plan.raw}\n`); // 非法 JSON 行,之后挂住等客户端断开
				return;
			}
			if (plan.oversize) {
				// 超过 4 MiB 的单行,不带换行
				socket.write(`{"t":"out","d":"${"x".repeat(5 * 1024 * 1024)}`);
				return;
			}
			if (plan.out) sendLine(socket, { t: "out", d: Buffer.from(plan.out, "utf8").toString("base64") });
			if (plan.err) sendLine(socket, { t: "err", d: Buffer.from(plan.err, "utf8").toString("base64") });
			if (plan.chunks) {
				for (const piece of plan.chunks) {
					if (piece.delayMs) await sleep(piece.delayMs);
					sendLine(socket, { t: "out", d: Buffer.from(piece.text, "utf8").toString("base64") });
				}
			}
			if (plan.noExit) {
				settled = true;
				socket.destroy(); // 在 exit 之前断开
				return;
			}
			sendLine(socket, {
				t: "exit",
				code: typeof plan.code === "number" ? plan.code : 0,
				signal: "",
				timed_out: plan.timedOut === true,
				reaped: 0,
				error: typeof plan.error === "string" ? plan.error : "",
			});
			settled = true;
		});
	});

	await new Promise((resolve, reject) => {
		server.once("error", reject);
		server.listen(socketPath, resolve);
	});

	return {
		socketPath,
		requests,
		async close() {
			serverShuttingDown = true;
			server.closeAllConnections?.();
			await new Promise((resolve) => server.close(() => resolve()));
			rmSync(socketPath, { force: true });
		},
	};
}

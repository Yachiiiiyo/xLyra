import { createServer } from "node:http";
import { appendFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

/**
 * Local multi-protocol LLM mock.
 *
 * startMock({ port=0, script, jsonlPath, tracePath }) -> { url, port, requests, close() }
 *
 * Records every request. Recognized POST paths (any prefix) get a minimal stream:
 *   /chat/completions          openai-completions
 *   /responses                 openai-responses
 *   /messages                  anthropic-messages
 *   :streamGenerateContent     google-generative-ai (SSE)
 *   :generateContent           google-generative-ai (JSON)
 * Anything else is recorded and answered 404.
 *
 * script(req, n) => step, where n is the 1-based count of recognized LLM posts.
 * step:
 *   { text }
 *   { toolCalls: [{ id, name, args }] }
 *   { text, toolCalls }
 *   { deltaCount, piece }  emit deltaCount copies of piece (default "x") instead of splitting text
 *   { pieceSize }          split text by this size (chat default remains 2)
 *   { status, body }
 *   { delayMs, ... }
 *   { hangMs, text? }  hold the socket; if text is set, write that chat SSE prefix first, then hold
 *   { usage }          optional { prompt_tokens, completion_tokens, total_tokens } on the success chunk
 *   { raw }            non-JSON body (invalid JSON tests)
 *   { drop: true }     write a partial chat SSE chunk, then destroy the socket
 *   { traceWrites }    record each SSE write drain time to tracePath
 */

function writeJsonl(jsonlPath, record) {
  if (!jsonlPath) return;
  mkdirSync(dirname(jsonlPath), { recursive: true });
  appendFileSync(jsonlPath, JSON.stringify(record) + "\n");
}

function splitPieces(text, size) {
  const pieces = [];
  const step = Math.max(1, size);
  for (let i = 0; i < text.length; i += step) pieces.push(text.slice(i, i + step));
  return pieces.length > 0 ? pieces : [""];
}

function chunk(id, model, choice, usage) {
  const payload = {
    id,
    object: "chat.completion.chunk",
    created: Math.floor(Date.now() / 1000),
    model,
    choices: choice ? [choice] : [],
  };
  if (usage) payload.usage = usage;
  return payload;
}

function writeSse(res, payload) {
  res.write(`data: ${JSON.stringify(payload)}\n\n`);
}

export function detectProtocol(pathname) {
  if (pathname.endsWith("/chat/completions")) return "openai-completions";
  if (pathname.endsWith("/responses")) return "openai-responses";
  if (pathname.endsWith("/messages")) return "anthropic-messages";
  if (pathname.includes(":streamGenerateContent") || pathname.includes(":generateContent")) {
    return "google-generative-ai";
  }
  return null;
}

function modelFrom(body, pathname) {
  if (body && typeof body === "object" && typeof body.model === "string" && body.model) return body.model;
  const match = pathname.match(/\/models\/([^/:]+):/);
  if (match) return decodeURIComponent(match[1]);
  return "mock";
}

function textPieces(step) {
  if (Number.isInteger(step.deltaCount) && step.deltaCount > 0) {
    const piece = typeof step.piece === "string" ? step.piece : "x";
    return Array.from({ length: step.deltaCount }, () => piece);
  }
  const text = typeof step.text === "string" ? step.text : "";
  if (text.length === 0) return [];
  const size = Number.isInteger(step.pieceSize) && step.pieceSize > 0 ? step.pieceSize : 2;
  return splitPieces(text, size);
}

function sseHeaders() {
  return {
    "Content-Type": "text/event-stream; charset=utf-8",
    "Cache-Control": "no-cache",
    Connection: "keep-alive",
  };
}

function makeEmitter(res, trace) {
  return async function emit(frame) {
    if (!trace) {
      res.write(frame);
      return;
    }
    const started = Date.now();
    await new Promise((resolve) => {
      res.write(frame, () => {
        trace({
          ts: new Date().toISOString(),
          started,
          finished: Date.now(),
          bytes: Buffer.byteLength(frame),
        });
        resolve();
      });
    });
  };
}

function dataFrame(payload) {
  return `data: ${JSON.stringify(payload)}\n\n`;
}

function eventFrame(event, payload) {
  return `event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function usagePayload(step) {
  const usage = step && step.usage && typeof step.usage === "object" ? step.usage : null;
  if (!usage) {
    return { prompt_tokens: 12, completion_tokens: 4, total_tokens: 16 };
  }
  const prompt = usage.prompt_tokens ?? usage.input ?? 12;
  const completion = usage.completion_tokens ?? usage.output ?? 4;
  return {
    prompt_tokens: prompt,
    completion_tokens: completion,
    total_tokens: usage.total_tokens ?? usage.total ?? prompt + completion,
  };
}

async function writeChatStream(res, model, step, emit) {
  const send = emit || ((frame) => res.write(frame));
  const id = `chatcmpl-mock-${Date.now().toString(36)}`;
  res.writeHead(200, sseHeaders());

  const text = typeof step.text === "string" ? step.text : "";
  const toolCalls = Array.isArray(step.toolCalls) ? step.toolCalls : [];
  let sentRole = false;
  const counted = Number.isInteger(step.deltaCount) && step.deltaCount > 0;
  const textChunks = counted ? textPieces(step) : text.length > 0 ? splitPieces(text, 2) : [];

  if (textChunks.length > 0) {
    for (const piece of textChunks) {
      const delta = { content: piece };
      if (!sentRole) {
        delta.role = "assistant";
        sentRole = true;
      }
      await send(dataFrame(chunk(id, model, { index: 0, delta, finish_reason: null })));
      if (step.gapMs) await sleep(step.gapMs);
    }
  }

  for (let index = 0; index < toolCalls.length; index++) {
    const call = toolCalls[index];
    const argsText = JSON.stringify(call.args ?? {});
    const pieces = splitPieces(argsText, 8);
    const firstDelta = {
      tool_calls: [
        {
          index,
          id: call.id,
          type: "function",
          function: { name: call.name, arguments: pieces[0] ?? "" },
        },
      ],
    };
    if (!sentRole) {
      firstDelta.role = "assistant";
      sentRole = true;
    }
    await send(dataFrame(chunk(id, model, { index: 0, delta: firstDelta, finish_reason: null })));
    for (const piece of pieces.slice(1)) {
      await send(
        dataFrame(
          chunk(id, model, {
            index: 0,
            delta: { tool_calls: [{ index, function: { arguments: piece } }] },
            finish_reason: null,
          }),
        ),
      );
    }
  }

  if (!sentRole) {
    await send(dataFrame(chunk(id, model, { index: 0, delta: { role: "assistant", content: "" }, finish_reason: null })));
  }

  const finish = toolCalls.length > 0 ? "tool_calls" : "stop";
  await send(dataFrame(chunk(id, model, { index: 0, delta: {}, finish_reason: finish })));
  await send(dataFrame(chunk(id, model, null, usagePayload(step))));
  await send("data: [DONE]\n\n");
  res.end();
}

async function writeResponsesStream(res, model, step, emit) {
  const id = `resp_mock_${Date.now().toString(36)}`;
  const msgId = `msg_${id}`;
  res.writeHead(200, sseHeaders());
  const toolCalls = Array.isArray(step.toolCalls) ? step.toolCalls : [];
  const pieces = textPieces(step);
  const text = pieces.join("");
  const usage = usagePayload(step);
  const output = [];

  await emit(
    eventFrame("response.created", {
      type: "response.created",
      response: { id, object: "response", status: "in_progress", model, output: [] },
    }),
  );

  if (pieces.length > 0) {
    const item = {
      type: "message",
      id: msgId,
      role: "assistant",
      status: "in_progress",
      content: [],
    };
    await emit(eventFrame("response.output_item.added", { type: "response.output_item.added", output_index: 0, item }));
    for (const piece of pieces) {
      await emit(
        eventFrame("response.output_text.delta", {
          type: "response.output_text.delta",
          output_index: 0,
          content_index: 0,
          delta: piece,
        }),
      );
    }
    const doneItem = {
      type: "message",
      id: msgId,
      role: "assistant",
      status: "completed",
      content: [{ type: "output_text", text, annotations: [] }],
    };
    output.push(doneItem);
    await emit(eventFrame("response.output_item.done", { type: "response.output_item.done", output_index: 0, item: doneItem }));
  }

  for (let index = 0; index < toolCalls.length; index++) {
    const call = toolCalls[index];
    const argsText = JSON.stringify(call.args ?? {});
    const outputIndex = output.length;
    const item = {
      type: "function_call",
      id: `fc_${call.id || index}`,
      call_id: call.id,
      name: call.name,
      arguments: "",
      status: "in_progress",
    };
    await emit(
      eventFrame("response.output_item.added", { type: "response.output_item.added", output_index: outputIndex, item }),
    );
    const argPieces = splitPieces(argsText, 8);
    for (const piece of argPieces) {
      await emit(
        eventFrame("response.function_call_arguments.delta", {
          type: "response.function_call_arguments.delta",
          output_index: outputIndex,
          delta: piece,
        }),
      );
    }
    const doneItem = { ...item, arguments: argsText, status: "completed" };
    output.push(doneItem);
    await emit(
      eventFrame("response.output_item.done", {
        type: "response.output_item.done",
        output_index: outputIndex,
        item: doneItem,
      }),
    );
  }

  await emit(
    eventFrame("response.completed", {
      type: "response.completed",
      response: {
        id,
        object: "response",
        status: "completed",
        model,
        output,
        usage: {
          input_tokens: usage.prompt_tokens,
          output_tokens: usage.completion_tokens,
          total_tokens: usage.total_tokens,
        },
      },
    }),
  );
  res.end();
}

async function writeAnthropicStream(res, model, step, emit) {
  const id = `msg_mock_${Date.now().toString(36)}`;
  res.writeHead(200, sseHeaders());
  const toolCalls = Array.isArray(step.toolCalls) ? step.toolCalls : [];
  const pieces = textPieces(step);
  const usage = usagePayload(step);
  await emit(
    eventFrame("message_start", {
      type: "message_start",
      message: {
        id,
        type: "message",
        role: "assistant",
        model,
        content: [],
        stop_reason: null,
        stop_sequence: null,
        usage: { input_tokens: usage.prompt_tokens, output_tokens: 1 },
      },
    }),
  );
  let index = 0;
  if (pieces.length > 0) {
    await emit(
      eventFrame("content_block_start", {
        type: "content_block_start",
        index,
        content_block: { type: "text", text: "" },
      }),
    );
    for (const piece of pieces) {
      await emit(
        eventFrame("content_block_delta", {
          type: "content_block_delta",
          index,
          delta: { type: "text_delta", text: piece },
        }),
      );
    }
    await emit(eventFrame("content_block_stop", { type: "content_block_stop", index }));
    index += 1;
  }
  for (const call of toolCalls) {
    const argsText = JSON.stringify(call.args ?? {});
    await emit(
      eventFrame("content_block_start", {
        type: "content_block_start",
        index,
        content_block: { type: "tool_use", id: call.id, name: call.name, input: {} },
      }),
    );
    await emit(
      eventFrame("content_block_delta", {
        type: "content_block_delta",
        index,
        delta: { type: "input_json_delta", partial_json: argsText },
      }),
    );
    await emit(eventFrame("content_block_stop", { type: "content_block_stop", index }));
    index += 1;
  }
  await emit(
    eventFrame("message_delta", {
      type: "message_delta",
      delta: { stop_reason: toolCalls.length > 0 ? "tool_use" : "end_turn", stop_sequence: null },
      usage: { output_tokens: usage.completion_tokens },
    }),
  );
  await emit(eventFrame("message_stop", { type: "message_stop" }));
  res.end();
}

function geminiChunk(parts, finishReason, step) {
  const usage = usagePayload(step);
  const candidate = {
    content: { role: "model", parts },
  };
  if (finishReason) candidate.finishReason = finishReason;
  return {
    candidates: [candidate],
    usageMetadata: {
      promptTokenCount: usage.prompt_tokens,
      candidatesTokenCount: usage.completion_tokens,
      totalTokenCount: usage.total_tokens,
    },
  };
}

async function writeGeminiStream(res, step, emit) {
  res.writeHead(200, sseHeaders());
  const toolCalls = Array.isArray(step.toolCalls) ? step.toolCalls : [];
  const pieces = textPieces(step);
  for (const piece of pieces) {
    await emit(dataFrame(geminiChunk([{ text: piece }], null, step)));
  }
  if (toolCalls.length > 0) {
    const parts = toolCalls.map((call) => ({
      functionCall: { id: call.id, name: call.name, args: call.args ?? {} },
    }));
    await emit(dataFrame(geminiChunk(parts, "STOP", step)));
  } else {
    await emit(dataFrame(geminiChunk(pieces.length > 0 ? [{ text: "" }] : [{ text: "" }], "STOP", step)));
  }
  res.end();
}

function writeGeminiJson(res, step) {
  const toolCalls = Array.isArray(step.toolCalls) ? step.toolCalls : [];
  const text = typeof step.text === "string" ? step.text : "";
  const parts = [];
  if (text) parts.push({ text });
  for (const call of toolCalls) {
    parts.push({ functionCall: { id: call.id, name: call.name, args: call.args ?? {} } });
  }
  if (parts.length === 0) parts.push({ text: "" });
  res.writeHead(200, { "Content-Type": "application/json" });
  res.end(JSON.stringify(geminiChunk(parts, "STOP", step)));
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

export function startMock({ port = 0, script, jsonlPath, tracePath } = {}) {
  if (typeof script !== "function") {
    throw new Error("startMock requires a script function");
  }

  const requests = [];
  let llmCount = 0;

  const server = createServer(async (req, res) => {
    req.on("error", () => {});
    res.on("error", () => {});
    const url = new URL(req.url || "/", "http://127.0.0.1");
    let raw = "";
    try {
      raw = await readBody(req);
    } catch (error) {
      res.writeHead(400, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: { message: String(error) } }));
      return;
    }

    let body = raw;
    if (raw.length > 0) {
      try {
        body = JSON.parse(raw);
      } catch {
        body = raw;
      }
    } else {
      body = null;
    }

    const protocol = req.method === "POST" ? detectProtocol(url.pathname) : null;
    const record = {
      ts: new Date().toISOString(),
      method: req.method || "GET",
      path: url.pathname + url.search,
      protocol,
      headers: { ...req.headers },
      body,
    };
    requests.push(record);
    writeJsonl(jsonlPath, record);

    if (!protocol) {
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: { message: "not found", path: url.pathname } }));
      return;
    }

    llmCount += 1;
    const n = llmCount;
    let step;
    try {
      step = await script(record, n);
    } catch (error) {
      res.writeHead(500, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: { message: `mock script threw: ${error}` } }));
      return;
    }
    step = step || { text: "" };

    if (step.delayMs) await sleep(step.delayMs);

    if (step.hangMs) {
      const prefix = typeof step.text === "string" ? step.text : "";
      if (prefix.length > 0) {
        res.writeHead(200, {
          "Content-Type": "text/event-stream; charset=utf-8",
          "Cache-Control": "no-cache",
          Connection: "keep-alive",
        });
        const pieces = splitPieces(prefix, 2);
        let sentRole = false;
        for (const piece of pieces) {
          const delta = { content: piece };
          if (!sentRole) {
            delta.role = "assistant";
            sentRole = true;
          }
          writeSse(
            res,
            chunk(`chatcmpl-hang-${Date.now().toString(36)}`, body && body.model ? body.model : "mock", {
              index: 0,
              delta,
              finish_reason: null,
            }),
          );
        }
      }
      const timer = setTimeout(() => {
        if (res.writableEnded || res.destroyed) return;
        if (!res.headersSent) res.writeHead(504, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: { message: "mock hang elapsed" } }));
      }, step.hangMs);
      const stop = () => clearTimeout(timer);
      req.on("close", stop);
      res.on("close", stop);
      return;
    }

    if (step.drop) {
      res.writeHead(200, { "Content-Type": "text/event-stream; charset=utf-8" });
      res.write(
        `data: ${JSON.stringify(
          chunk("chatcmpl-drop", body && body.model ? body.model : "mock", {
            index: 0,
            delta: { role: "assistant", content: "partial" },
            finish_reason: null,
          }),
        )}\n\n`,
      );
      req.socket.destroy();
      return;
    }

    if (typeof step.raw === "string") {
      res.writeHead(step.status ?? 200, { "Content-Type": "application/json" });
      res.end(step.raw);
      return;
    }

    if (step.status && step.status !== 200) {
      res.writeHead(step.status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(step.body ?? { error: { message: `status ${step.status}` } }));
      return;
    }

    const model = modelFrom(body, url.pathname);
    const trace = step.traceWrites
      ? (entry) => writeJsonl(tracePath, { ...entry, n, path: record.path, protocol })
      : null;
    if (protocol === "openai-completions" && body && typeof body === "object" && body.stream === false) {
      const text = step.text || "";
      const toolCalls = step.toolCalls || [];
      const message = { role: "assistant", content: text || null };
      if (toolCalls.length > 0) {
        message.tool_calls = toolCalls.map((call) => ({
          id: call.id,
          type: "function",
          function: { name: call.name, arguments: JSON.stringify(call.args ?? {}) },
        }));
      }
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(
        JSON.stringify({
          id: "chatcmpl-mock",
          object: "chat.completion",
          model,
          choices: [
            {
              index: 0,
              message,
              finish_reason: toolCalls.length > 0 ? "tool_calls" : "stop",
            },
          ],
          usage: usagePayload(step),
        }),
      );
      return;
    }

    if (protocol === "openai-responses") {
      await writeResponsesStream(res, model, step, makeEmitter(res, trace));
      return;
    }
    if (protocol === "anthropic-messages") {
      await writeAnthropicStream(res, model, step, makeEmitter(res, trace));
      return;
    }
    if (protocol === "google-generative-ai") {
      if (url.pathname.includes(":streamGenerateContent")) {
        await writeGeminiStream(res, step, makeEmitter(res, trace));
      } else {
        writeGeminiJson(res, step);
      }
      return;
    }
    await writeChatStream(res, model, step, trace ? makeEmitter(res, trace) : null);
  });

  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", () => {
      const address = server.address();
      const bound = typeof address === "object" && address ? address.port : port;
      resolve({
        url: `http://127.0.0.1:${bound}`,
        port: bound,
        requests,
        close() {
          return new Promise((done, fail) => {
            server.close((error) => (error ? fail(error) : done()));
          });
        },
      });
    });
  });
}

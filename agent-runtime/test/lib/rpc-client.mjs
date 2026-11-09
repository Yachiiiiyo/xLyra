import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { mkdirSync } from "node:fs";
import { dirname } from "node:path";
import { StringDecoder } from "node:string_decoder";
import { cleanEnv, defaultPath } from "./env.mjs";

/**
 * Pi RPC client. Frames stdout on LF only (optional trailing CR). Does not use readline.
 *
 * startRpc({ entry, args, env, stderrFile, cwd, eventLog })
 *   entry: absolute path to dist/bundle/cli.js (or the pi bin js)
 *   env: merged on top of cleanEnv defaults. process.env is not inherited.
 */
export function startRpc({
  entry,
  args = [],
  env,
  stderrFile,
  cwd,
  eventLog,
  commandTimeoutMs = 30000,
} = {}) {
  if (!entry) throw new Error("startRpc requires entry");
  if (!cwd) throw new Error("startRpc requires cwd");
  if (!stderrFile) throw new Error("startRpc requires stderrFile");

  const merged = cleanEnv({});
  if (env) Object.assign(merged, env);
  if (!merged.PATH) merged.PATH = defaultPath();
  if (!merged.HOME) throw new Error("RPC env is missing HOME");
  if (!merged.PI_CODING_AGENT_DIR) throw new Error("RPC env is missing PI_CODING_AGENT_DIR");
  for (const key of Object.keys(merged)) {
    if (merged[key] === undefined || merged[key] === null) delete merged[key];
  }

  mkdirSync(dirname(stderrFile), { recursive: true });
  if (eventLog) mkdirSync(dirname(eventLog), { recursive: true });

  const stderrStream = createWriteStream(stderrFile, { flags: "a" });
  const eventStream = eventLog ? createWriteStream(eventLog, { flags: "a" }) : null;
  const stderrChunks = [];
  let stderrBytes = 0;

  const spawnedAt = Date.now();
  const child = spawn(process.execPath, [entry, ...args], {
    cwd,
    env: merged,
    stdio: ["pipe", "pipe", "pipe"],
  });

  child.stderr.on("data", (chunk) => {
    stderrStream.write(chunk);
    stderrBytes += chunk.length;
    stderrChunks.push(chunk);
    if (stderrBytes > 256 * 1024) {
      while (stderrBytes > 64 * 1024 && stderrChunks.length > 1) {
        stderrBytes -= stderrChunks.shift().length;
      }
    }
  });

  const events = [];
  const records = [];
  const noise = [];
  const listeners = new Map();
  const pending = new Map();
  let nextId = 0;
  let settledExit = null;

  const stderrClosed = new Promise((resolve) => child.stderr.on("close", resolve));
  const exitPromise = new Promise((resolve) => {
    child.on("error", (error) => {
      if (settledExit) return;
      settledExit = { code: null, signal: null, durationMs: Date.now() - spawnedAt, error: String(error) };
      for (const waiter of pending.values()) waiter.reject(error);
      pending.clear();
      resolve(settledExit);
    });
    child.on("exit", (code, signal) => {
      settledExit = { code, signal, durationMs: Date.now() - spawnedAt };
      const error = new Error(`pi exited code=${code} signal=${signal}`);
      for (const waiter of pending.values()) waiter.reject(error);
      pending.clear();
      resolve(settledExit);
    });
  });

  function stderrText() {
    return Buffer.concat(stderrChunks).toString("utf8");
  }

  function emit(type, record) {
    const set = listeners.get(type);
    if (!set) return;
    for (const fn of [...set]) fn(record);
  }

  function handleRecord(record) {
    records.push(record);
    if (eventStream) eventStream.write(JSON.stringify(record) + "\n");
    if (record && record.type === "response" && record.id && pending.has(record.id)) {
      const waiter = pending.get(record.id);
      pending.delete(record.id);
      waiter.resolve(record);
      return;
    }
    events.push(record);
    emit(record.type, record);
    emit("*", record);
  }

  const decoder = new StringDecoder("utf8");
  let buffer = "";

  function consume(finalChunk) {
    buffer += finalChunk;
    while (true) {
      const newlineIndex = buffer.indexOf("\n");
      if (newlineIndex === -1) return;
      let line = buffer.slice(0, newlineIndex);
      buffer = buffer.slice(newlineIndex + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);
      if (line.length === 0) continue;
      try {
        handleRecord(JSON.parse(line));
      } catch {
        noise.push(line);
      }
    }
  }

  child.stdout.on("data", (chunk) => {
    consume(decoder.write(chunk));
  });
  child.stdout.on("end", () => {
    consume(decoder.end());
    if (buffer.length > 0) {
      let line = buffer;
      buffer = "";
      if (line.endsWith("\r")) line = line.slice(0, -1);
      if (line.length === 0) return;
      try {
        handleRecord(JSON.parse(line));
      } catch {
        noise.push(line);
      }
    }
  });

  function on(type, fn) {
    if (!listeners.has(type)) listeners.set(type, new Set());
    listeners.get(type).add(fn);
    return () => listeners.get(type)?.delete(fn);
  }

  function send(cmd, timeoutMs = commandTimeoutMs) {
    if (!child.stdin || child.stdin.destroyed || !child.stdin.writable) {
      return Promise.reject(new Error(`stdin is not writable. stderr:\n${stderrText()}`));
    }
    if (settledExit) {
      return Promise.reject(new Error(`pi already exited code=${settledExit.code}. stderr:\n${stderrText()}`));
    }
    const id = `req_${++nextId}`;
    const full = { ...cmd, id };
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new Error(`timeout waiting for ${cmd.type} id=${id}. stderr:\n${stderrText()}`));
      }, timeoutMs);
      pending.set(id, {
        resolve(response) {
          clearTimeout(timer);
          resolve(response);
        },
        reject(error) {
          clearTimeout(timer);
          reject(error);
        },
      });
      child.stdin.write(JSON.stringify(full) + "\n", (error) => {
        if (!error) return;
        clearTimeout(timer);
        pending.delete(id);
        reject(error);
      });
    });
  }

  function extensionUiRespond(id, payload) {
    const record = { type: "extension_ui_response", id, ...payload };
    child.stdin.write(JSON.stringify(record) + "\n");
  }

  async function promptAndWait(message, options = {}) {
    const { images, streamingBehavior, timeoutMs = 90000 } = options;
    const startIndex = events.length;
    let settle;
    const settled = new Promise((resolve) => {
      settle = resolve;
    });
    const unsubscribe = on("agent_settled", (event) => settle(event));
    const command = { type: "prompt", message };
    if (images) command.images = images;
    if (streamingBehavior) command.streamingBehavior = streamingBehavior;
    const response = await send(command, timeoutMs);
    if (!response.success) {
      unsubscribe();
      const error = new Error(response.error || "prompt failed");
      error.response = response;
      throw error;
    }
    const disposition = response.data?.disposition;
    if (disposition !== "started" && disposition !== "queued" && disposition !== "handled") {
      unsubscribe();
      const error = new Error(`unexpected prompt disposition: ${JSON.stringify(response.data)}`);
      error.response = response;
      throw error;
    }
    if (disposition !== "handled") {
      const already = events.slice(startIndex).some((event) => event.type === "agent_settled");
      if (!already) {
        const timer = setTimeout(() => settle(null), timeoutMs);
        const found = await settled;
        clearTimeout(timer);
        if (!found) {
          unsubscribe();
          throw new Error(`timeout waiting for agent_settled. stderr:\n${stderrText()}`);
        }
      }
    }
    unsubscribe();
    return {
      response,
      disposition,
      events: events.slice(startIndex),
    };
  }

  async function waitExit(fromMs) {
    const result = await exitPromise;
    return { ...result, durationMs: Date.now() - fromMs };
  }

  async function close() {
    const fromMs = Date.now();
    if (child.stdin && !child.stdin.destroyed) child.stdin.end();
    let result = await Promise.race([
      waitExit(fromMs),
      new Promise((resolve) =>
        setTimeout(() => {
          resolve(null);
        }, 20000),
      ),
    ]);
    if (!result) {
      child.kill("SIGKILL");
      result = await Promise.race([
        waitExit(fromMs),
        new Promise((resolve) =>
          setTimeout(
            () => resolve({ code: null, signal: "SIGKILL", durationMs: Date.now() - fromMs, forced: true }),
            3000,
          ),
        ),
      ]);
      result.forced = true;
    }
    await Promise.race([stderrClosed, new Promise((resolve) => setTimeout(resolve, 1000))]);
    await finishIo();
    return result;
  }

  function kill(signal = "SIGTERM") {
    const fromMs = Date.now();
    child.kill(signal);
    return waitExit(fromMs);
  }

  function finishIo() {
    return new Promise((resolve) => {
      stderrStream.end(() => {
        if (!eventStream) {
          resolve();
          return;
        }
        eventStream.end(() => resolve());
      });
    });
  }

  return {
    pid: child.pid,
    spawnedAt,
    events,
    records,
    noise,
    on,
    send,
    promptAndWait,
    extensionUiRespond,
    close,
    kill,
    stderrText,
    child,
  };
}

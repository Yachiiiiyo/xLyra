import type { PluginMeta, ProtocolBuildContext, ProtocolSignInput, SignRequestResult } from "../types/xlyra";

export const meta: PluginMeta = { apiVersion: 1, id: "example.request" };

export function signRequest(ctx: ProtocolBuildContext, _req: ProtocolSignInput): SignRequestResult {
  return { headers: { "X-Request-Plugin": ctx.sessionKey ? "session" : "anon" } };
}

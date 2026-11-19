import type {
  CredentialCheckDecision,
  ErrorClassifyContext,
  ErrorClassifyInput,
  ErrorClassifyResult,
  PluginMeta,
  ProbeContext,
  ProbeStep,
  RewritePayloadContext,
} from "../types/xlyra";

export const meta: PluginMeta = { apiVersion: 1, id: "example.transport" };

export function rewritePayload(_ctx: RewritePayloadContext, payload: Record<string, unknown>): Record<string, unknown> {
  return { ...payload, metadata: { ...(payload.metadata as Record<string, unknown> | undefined), overlay: "transport" } };
}

export function check(_ctx: ProbeContext, steps: ProbeStep[]): CredentialCheckDecision {
  if (steps.length === 0) {
    return { request: { method: "GET", path: "/v1/ping" } };
  }
  return { result: { status: steps[0].response.status === 200 ? "ok" : "invalid" } };
}

export function classifyError(_ctx: ErrorClassifyContext, input: ErrorClassifyInput): ErrorClassifyResult {
  if (input.code === "upstream_overloaded") {
    return { class: "transient", reason: "overloaded" };
  }
  return { class: "unknown" };
}

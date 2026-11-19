import type {
  ErrorClassifyContext,
  ErrorClassifyInput,
  ErrorClassifyResult,
  ModelListDecision,
  PluginMeta,
  ProbeContext,
  ProbeDecision,
  ProbeStep,
} from "../types/xlyra";

export const meta: PluginMeta = { apiVersion: 1, id: "example.hooks" };

export function probe(ctx: ProbeContext, steps: ProbeStep[]): ProbeDecision {
  if (steps.length === 0) {
    return { request: { method: "GET", path: "/v1/quota" } };
  }
  const body = steps[0].response.json as { remaining?: unknown } | null;
  const remaining = Number(body?.remaining ?? 0);
  const prev = Number(storage.get("probeCount") ?? 0);
  storage.set("probeCount", prev + 1);
  return {
    result: {
      kind: "balance",
      plan: String(ctx.credentialType ?? "api_key"),
      entries: [{ label: "balance", unit: "usd", remaining, limit: 100, used: 100 - remaining }],
    },
  };
}

export function classifyError(_ctx: ErrorClassifyContext, input: ErrorClassifyInput): ErrorClassifyResult {
  if (input.status === 429) {
    return { class: "limited", reason: input.message };
  }
  return { class: "unknown" };
}

export function listModels(_ctx: ProbeContext, steps: ProbeStep[]): ModelListDecision {
  if (steps.length === 0) {
    return { request: { method: "GET", path: "/v1/models" } };
  }
  const body = steps[0].response.json as { data?: { id?: string }[] } | null;
  const rows = body?.data ?? [];
  return { result: { models: rows.map((row) => ({ name: String(row.id ?? "") })) } };
}

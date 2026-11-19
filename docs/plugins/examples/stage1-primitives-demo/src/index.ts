import type {
  PluginMeta,
  ProbeContext,
  ProbeDecision,
  ProbeStep,
  RewritePayloadContext,
} from "../types/xlyra";

export const meta: PluginMeta = { apiVersion: 1, id: "example.primitives", kind: "site_provider" };

export function probe(ctx: ProbeContext, steps: ProbeStep[]): ProbeDecision {
  const version = String(ctx.settings?.clientVersion ?? "2.4.0");
  if (steps.length === 0) {
    const nonce = encoding.hexEncode(bytes.fromUTF8("probe:" + version));
    return {
      request: {
        method: "GET",
        path: "/v1/quota",
        headers: { "X-Client-Nonce": nonce },
      },
    };
  }
  const body = steps[0].response.json as { remaining?: unknown; limit?: unknown } | null;
  const remaining = Number(body?.remaining ?? 0);
  const limit = Number(body?.limit ?? 0);
  const prevProbes = Number(storage.get("probeCount") ?? 0);
  storage.set("probeCount", prevProbes + 1);
  return {
    result: {
      kind: "balance",
      plan: "v" + version,
      entries: [{ label: "balance", unit: "usd", remaining, limit, used: limit - remaining }],
    },
  };
}

export function rewritePayload(
  ctx: RewritePayloadContext,
  payload: Record<string, unknown>,
): Record<string, unknown> {
  const version = String(ctx.settings?.clientVersion ?? "2.4.0");
  const key = bytes.fromUTF8("0123456789abcdef0123456789abcdef");
  const iv = bytes.fromUTF8("0123456789ab");
  const sealed = crypto.aesEncrypt("aes-256-gcm", key, iv, version);
  const opened = bytes.toUTF8(crypto.aesDecrypt("aes-256-gcm", key, iv, sealed));
  const sig = crypto.hmac("sha256", "demo-key", opened, "hex");
  return {
    ...payload,
    client_tag: opened + ":" + sig.slice(0, 12),
  };
}

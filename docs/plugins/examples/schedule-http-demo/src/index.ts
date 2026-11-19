import type { PluginMeta, ProbeStep, ScheduleContext, ScheduleDecision } from "../types/xlyra";

export const meta: PluginMeta = { apiVersion: 1, id: "example.schedule" };

export function onSchedule(ctx: ScheduleContext, steps: ProbeStep[]): ScheduleDecision {
  if (steps.length === 0) {
    return {
      request: {
        method: "POST",
        path: "/v1/checkin",
        body: { timestamp: ctx.now },
      },
    };
  }
  const body = steps[0].response.json as { ok?: boolean } | null;
  if (!body?.ok) {
    return { error: "checkin failed" };
  }
  const runs = Number(storage.get("checkinRuns") ?? 0) + 1;
  storage.set("checkinRuns", runs);
  return { result: { ok: true, runs } };
}

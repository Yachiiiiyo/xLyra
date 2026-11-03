import { dirname } from "node:path";

/** Directory that contains the node binary, plus the usual system bins. */
export function defaultPath() {
  return `${dirname(process.execPath)}:/usr/bin:/bin:/usr/sbin:/sbin`;
}

/**
 * env -i style environment for Pi.
 * Only the variables named in tasks/common.md, plus `extra` for a single test.
 * Caller values in `extra` override the defaults. Nothing is copied from process.env.
 */
export function cleanEnv({ home, agentDir, sessionDir, extra } = {}) {
  const env = {
    PATH: defaultPath(),
    PI_OFFLINE: "1",
    PI_SKIP_VERSION_CHECK: "1",
    PI_TELEMETRY: "0",
  };
  if (home) env.HOME = home;
  if (agentDir) env.PI_CODING_AGENT_DIR = agentDir;
  if (sessionDir) env.PI_CODING_AGENT_SESSION_DIR = sessionDir;
  if (extra) {
    for (const [key, value] of Object.entries(extra)) {
      if (value === undefined || value === null) continue;
      env[key] = String(value);
    }
  }
  return env;
}

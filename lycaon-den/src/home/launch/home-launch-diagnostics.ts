import { recordPerfDiagnostic } from "../../chat/stream/den-main-thread-perf.ts";

/** Points on the way from Home submission to the chat's first turn. */
export type LaunchMark = "submit" | "first_render" | "local_commit" | "binding" | "admission";

const marks = new Map<string, Map<LaunchMark, number>>();

function now(): number {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

/**
 * Records a mark once per launch with its offset from submission. Diagnostics
 * carry ids and timing only, never the idea or its files.
 */
export function markLaunch(launchId: string, mark: LaunchMark, detail?: { code?: string }): void {
  let launch = marks.get(launchId);
  if (!launch) {
    launch = new Map();
    marks.set(launchId, launch);
  }
  if (launch.has(mark)) return;
  const at = now();
  launch.set(mark, at);
  const submitted = launch.get("submit") ?? at;
  recordPerfDiagnostic("home_launch", {
    launch_id: launchId,
    mark,
    since_submit_ms: Math.round(at - submitted),
    ...(detail?.code ? { code: detail.code } : {}),
  });
}

/** Offsets from submission for each recorded mark. */
export function launchTimings(launchId: string): Partial<Record<LaunchMark, number>> {
  const launch = marks.get(launchId);
  if (!launch) return {};
  const submitted = launch.get("submit") ?? 0;
  return Object.fromEntries([...launch].map(([mark, at]) => [mark, Math.round(at - submitted)]));
}

/** Records a failure with its typed code for diagnostics. */
export function noteLaunchFailure(launchId: string, code: string): void {
  recordPerfDiagnostic("home_launch_failure", { launch_id: launchId, code });
}
